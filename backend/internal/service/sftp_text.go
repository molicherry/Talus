package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/pkg/sftp"
)

const MaxSFTPTextBytes int64 = 1 << 20

type SFTPText struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Version string `json:"version"`
}

func textVersion(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func remoteText(c *sftp.Client, p string) (*SFTPText, error) {
	info, err := c.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, SFTPError("sftp_not_regular")
	}
	if info.Size() > MaxSFTPTextBytes {
		return nil, SFTPError("sftp_text_too_large")
	}
	file, err := c.Open(p)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxSFTPTextBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > MaxSFTPTextBytes {
		return nil, SFTPError("sftp_text_too_large")
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, SFTPError("sftp_not_text")
	}
	return &SFTPText{Path: p, Content: string(data), Version: textVersion(data)}, nil
}
func (s *SFTPService) ReadText(ctx context.Context, id uint, p string) (*SFTPText, error) {
	p, err := remotePath(p)
	if err != nil {
		return nil, err
	}
	var result *SFTPText
	err = s.withClient(ctx, id, false, func(_ context.Context, c *sftp.Client) error {
		var err error
		result, err = remoteText(c, p)
		return err
	})
	return result, err
}
func (s *SFTPService) SaveText(ctx context.Context, id uint, p, content, version string) (*SFTPText, error) {
	p, err := mutablePath(p)
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > MaxSFTPTextBytes {
		return nil, SFTPError("sftp_text_too_large")
	}
	if !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return nil, SFTPError("sftp_not_text")
	}
	if len(version) != 71 || !strings.HasPrefix(version, "sha256:") {
		return nil, SFTPError("sftp_conflict")
	}
	var result *SFTPText
	err = s.withClient(ctx, id, false, func(ctx context.Context, c *sftp.Client) error {
		previous, err := pendingDestination(c, p, true)
		if err != nil {
			return err
		}
		if previous == nil {
			return SFTPError("sftp_conflict")
		}
		current, err := remoteText(c, p)
		if err != nil {
			return err
		}
		if current.Version != version {
			return SFTPError("sftp_conflict")
		}
		tmp := path.Join(path.Dir(p), ".talus-upload-"+randomFileSuffix())
		f, err := c.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			_ = f.Close()
			if !committed {
				_ = c.Remove(tmp)
			}
		}()
		if err = c.Chmod(tmp, 0600); err != nil {
			return err
		}
		if _, err = io.Copy(f, strings.NewReader(content)); err != nil {
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		current, err = remoteText(c, p)
		if err != nil {
			return err
		}
		if current.Version != version {
			return SFTPError("sftp_conflict")
		}
		if err = commitRemoteFile(c, tmp, p, previous); err != nil {
			return err
		}
		committed = true
		result = &SFTPText{Path: p, Content: content, Version: textVersion([]byte(content))}
		return nil
	})
	return result, err
}
