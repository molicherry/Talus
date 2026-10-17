package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
)

const MaxSFTPChunkBytes int64 = 1 << 20
const sftpUploadLifetime = 24 * time.Hour

type sftpRouteContextKey struct{}
type sftpWireContextKey struct{}

func sftpRouteFingerprint(ctx context.Context) string {
	value, _ := ctx.Value(sftpRouteContextKey{}).(string)
	return value
}
func setSFTPWireBudget(ctx context.Context, limit int64) {
	if budget, ok := ctx.Value(sftpWireContextKey{}).(*sftpBoundedReader); ok {
		budget.remaining.Store(limit)
	}
}
func sftpOwner(ctx context.Context) (string, error) {
	p := mw.GetPrincipal(ctx)
	if p.AuthType == "api_key" && p.APIKeyID != nil {
		return fmt.Sprintf("key:%d", *p.APIKeyID), nil
	}
	if p.AuthType == "jwt" && p.UserID != nil && *p.UserID != 0 {
		return fmt.Sprintf("user:%d", *p.UserID), nil
	}
	return "", server.ErrForbidden
}

type sftpFileVersion struct {
	Size     int64  `json:"size"`
	Mode     uint32 `json:"mode"`
	Modified int64  `json:"modified"`
	SHA256   string `json:"sha256"`
}
type sftpUploadTicket struct {
	Version     int              `json:"v"`
	Owner       string           `json:"owner"`
	ServerID    uint             `json:"server_id"`
	Route       string           `json:"route"`
	Destination string           `json:"destination"`
	Temporary   string           `json:"temporary"`
	Size        int64            `json:"size"`
	SHA256      string           `json:"sha256"`
	ExpiresAt   int64            `json:"expires_at"`
	Previous    *sftpFileVersion `json:"previous,omitempty"`
}
type SFTPUpload struct {
	Token     string    `json:"token,omitempty"`
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	Offset    int64     `json:"offset"`
	SHA256    string    `json:"sha256"`
	ExpiresAt time.Time `json:"expires_at"`
	Completed bool      `json:"completed,omitempty"`
}

func (s *SFTPService) signUpload(ticket *sftpUploadTicket) string {
	data, _ := json.Marshal(ticket)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("talus-sftp-upload-v1\x00"))
	mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *SFTPService) readUpload(ctx context.Context, id uint, token string, allowExpired ...bool) (*sftpUploadTicket, error) {
	if len(token) > 16384 {
		return nil, SFTPError("sftp_invalid_upload")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, SFTPError("sftp_invalid_upload")
	}
	data, e1 := base64.RawURLEncoding.DecodeString(parts[0])
	sig, e2 := base64.RawURLEncoding.DecodeString(parts[1])
	if e1 != nil || e2 != nil {
		return nil, SFTPError("sftp_invalid_upload")
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("talus-sftp-upload-v1\x00"))
	mac.Write(data)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, SFTPError("sftp_invalid_upload")
	}
	var ticket sftpUploadTicket
	if json.Unmarshal(data, &ticket) != nil || ticket.Version != 1 || ticket.ServerID != id || ticket.Size < 0 || ticket.Size > MaxSFTPFileBytes {
		return nil, SFTPError("sftp_invalid_upload")
	}
	owner, err := sftpOwner(ctx)
	if err != nil {
		return nil, err
	}
	if owner != ticket.Owner {
		return nil, server.ErrForbidden
	}
	if time.Now().Unix() > ticket.ExpiresAt && !(len(allowExpired) > 0 && allowExpired[0]) {
		return nil, SFTPError("sftp_upload_expired")
	}
	return &ticket, nil
}
func uploadStatus(ticket *sftpUploadTicket, offset int64) *SFTPUpload {
	return &SFTPUpload{Path: ticket.Destination, Size: ticket.Size, Offset: offset, SHA256: ticket.SHA256, ExpiresAt: time.Unix(ticket.ExpiresAt, 0).UTC()}
}
func validateUploadRoute(ctx context.Context, ticket *sftpUploadTicket) error {
	if sftpRouteFingerprint(ctx) != ticket.Route {
		return SFTPError("sftp_conflict")
	}
	return nil
}
func hashRemoteFile(c *sftp.Client, p string, limit int64) (string, error) {
	info, err := c.Lstat(p)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", SFTPError("sftp_not_regular")
	}
	if info.Size() > limit {
		return "", SFTPError("sftp_too_large")
	}
	file, err := c.Open(p)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	n, err := io.CopyBuffer(h, io.LimitReader(file, limit+1), make([]byte, 32<<10))
	if err != nil {
		return "", err
	}
	if n != info.Size() {
		return "", SFTPError("sftp_conflict")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (s *SFTPService) StartUpload(ctx context.Context, id uint, destination string, size int64, digest string, overwrite bool) (*SFTPUpload, error) {
	destination, err := mutablePath(destination)
	if err != nil {
		return nil, err
	}
	hash, err := hex.DecodeString(digest)
	if err != nil || len(hash) != sha256.Size {
		return nil, SFTPError("sftp_invalid_upload")
	}
	if size < 0 || size > MaxSFTPFileBytes {
		return nil, SFTPError("sftp_too_large")
	}
	owner, err := sftpOwner(ctx)
	if err != nil {
		return nil, err
	}
	var result *SFTPUpload
	err = s.withClient(ctx, id, false, func(ctx context.Context, c *sftp.Client) error {
		parent, err := c.RealPath(path.Dir(destination))
		if err != nil {
			return err
		}
		destination = path.Join(parent, path.Base(destination))
		previous, err := pendingDestination(c, destination, overwrite)
		if err != nil {
			return err
		}
		ticket := &sftpUploadTicket{Version: 1, Owner: owner, ServerID: id, Route: sftpRouteFingerprint(ctx), Destination: destination, Temporary: path.Join(parent, ".talus-upload-"+randomFileSuffix()), Size: size, SHA256: strings.ToLower(digest), ExpiresAt: time.Now().Add(sftpUploadLifetime).Unix()}
		if previous != nil {
			hash, err := hashRemoteFile(c, destination, MaxSFTPFileBytes)
			if err != nil {
				return err
			}
			ticket.Previous = &sftpFileVersion{Size: previous.Size(), Mode: uint32(previous.Mode()), Modified: previous.ModTime().Unix(), SHA256: hash}
		}
		token := s.signUpload(ticket)
		if len(token) > 16384 {
			return SFTPError("sftp_invalid_path")
		}
		f, err := c.OpenFile(ticket.Temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
		if err != nil {
			return err
		}
		if err = c.Chmod(ticket.Temporary, 0600); err != nil {
			_ = f.Close()
			_ = c.Remove(ticket.Temporary)
			return err
		}
		if err = f.Close(); err != nil {
			_ = c.Remove(ticket.Temporary)
			return err
		}
		result = uploadStatus(ticket, 0)
		result.Token = token
		return nil
	})
	return result, err
}
func (s *SFTPService) UploadStatus(ctx context.Context, id uint, token string) (*SFTPUpload, error) {
	ticket, err := s.readUpload(ctx, id, token)
	if err != nil {
		return nil, err
	}
	var result *SFTPUpload
	err = s.withClient(ctx, id, false, func(ctx context.Context, c *sftp.Client) error {
		if err := validateUploadRoute(ctx, ticket); err != nil {
			return err
		}
		info, err := c.Lstat(ticket.Temporary)
		if errors.Is(err, fs.ErrNotExist) {
			committed, checkErr := completedUploadMatches(c, ticket)
			if checkErr == nil && committed {
				result = uploadStatus(ticket, ticket.Size)
				result.Completed = true
				return nil
			}
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > ticket.Size {
			return SFTPError("sftp_conflict")
		}
		result = uploadStatus(ticket, info.Size())
		return nil
	})
	return result, err
}
func (s *SFTPService) UploadChunk(ctx context.Context, id uint, token string, offset, size int64, body io.Reader) (*SFTPUpload, error) {
	ticket, err := s.readUpload(ctx, id, token)
	if err != nil {
		return nil, err
	}
	if offset < 0 || size <= 0 || size > MaxSFTPChunkBytes || offset > ticket.Size || size > ticket.Size-offset {
		return nil, SFTPError("sftp_invalid_upload")
	}
	var result *SFTPUpload
	err = s.withClient(ctx, id, false, func(ctx context.Context, c *sftp.Client) error {
		if err := validateUploadRoute(ctx, ticket); err != nil {
			return err
		}
		info, err := c.Lstat(ticket.Temporary)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != offset {
			return SFTPError("sftp_conflict")
		}
		f, err := c.OpenFile(ticket.Temporary, os.O_WRONLY)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err = f.Seek(offset, io.SeekStart); err != nil {
			return err
		}
		n, err := io.CopyBuffer(f, io.LimitReader(body, size+1), make([]byte, 32<<10))
		if err != nil {
			return err
		}
		if n != size {
			return SFTPError("sftp_size_mismatch")
		}
		if err = f.Close(); err != nil {
			return err
		}
		result = uploadStatus(ticket, offset+n)
		return ctx.Err()
	})
	return result, err
}
func (s *SFTPService) CompleteUpload(ctx context.Context, id uint, token string) error {
	ticket, err := s.readUpload(ctx, id, token)
	if err != nil {
		return err
	}
	return s.withClient(ctx, id, false, func(ctx context.Context, c *sftp.Client) error {
		if err := validateUploadRoute(ctx, ticket); err != nil {
			return err
		}
		info, err := c.Lstat(ticket.Temporary)
		if errors.Is(err, fs.ErrNotExist) {
			committed, checkErr := completedUploadMatches(c, ticket)
			if checkErr == nil && committed {
				return nil
			}
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != ticket.Size {
			return SFTPError("sftp_size_mismatch")
		}
		digest, err := hashRemoteFile(c, ticket.Temporary, MaxSFTPFileBytes)
		if err != nil {
			return err
		}
		if digest != ticket.SHA256 {
			return SFTPError("sftp_checksum_mismatch")
		}
		if ticket.Previous == nil {
			if _, err := c.Lstat(ticket.Destination); err == nil {
				return SFTPError("sftp_exists")
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if _, ok := c.HasExtension("hardlink@openssh.com"); ok {
				if err := c.Link(ticket.Temporary, ticket.Destination); err != nil {
					return err
				}
				_ = c.Remove(ticket.Temporary)
				return nil
			}
			return c.Rename(ticket.Temporary, ticket.Destination)
		}
		previous := ticket.Previous
		current, err := c.Lstat(ticket.Destination)
		if err != nil || !current.Mode().IsRegular() || current.Size() != previous.Size || uint32(current.Mode()) != previous.Mode || current.ModTime().Unix() != previous.Modified {
			return SFTPError("sftp_conflict")
		}
		digest, err = hashRemoteFile(c, ticket.Destination, MaxSFTPFileBytes)
		if err != nil {
			return err
		}
		if digest != previous.SHA256 {
			return SFTPError("sftp_conflict")
		}
		if err = c.Chmod(ticket.Temporary, current.Mode().Perm()); err != nil {
			return err
		}
		return c.PosixRename(ticket.Temporary, ticket.Destination)
	})
}
func (s *SFTPService) CancelUpload(ctx context.Context, id uint, token string) error {
	ticket, err := s.readUpload(ctx, id, token, true)
	if err != nil {
		return err
	}
	return s.withClient(ctx, id, true, func(ctx context.Context, c *sftp.Client) error {
		if err := validateUploadRoute(ctx, ticket); err != nil {
			return err
		}
		err := c.Remove(ticket.Temporary)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	})
}

func completedUploadMatches(c *sftp.Client, ticket *sftpUploadTicket) (bool, error) {
	info, err := c.Lstat(ticket.Destination)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() != ticket.Size {
		return false, nil
	}
	digest, err := hashRemoteFile(c, ticket.Destination, MaxSFTPFileBytes)
	return err == nil && digest == ticket.SHA256, err
}
