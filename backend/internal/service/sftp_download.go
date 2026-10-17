package service

import (
	"context"
	"github.com/pkg/sftp"
	"io"
	"strconv"
	"strings"
)

type SFTPDownloadRequest struct {
	Range   string
	IfRange string
}
type SFTPDownload struct {
	Entry                SFTPEntry
	Start, Length, Total int64
	ETag                 string
	Partial              bool
}

func sftpByteRange(value string, size int64) (int64, int64, error) {
	if !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return 0, 0, SFTPError("sftp_invalid_range")
	}
	parts := strings.Split(strings.TrimPrefix(value, "bytes="), "-")
	if len(parts) != 2 || size <= 0 {
		return 0, 0, SFTPError("sftp_invalid_range")
	}
	if parts[0] == "" {
		suffix, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, SFTPError("sftp_invalid_range")
		}
		suffix = min(suffix, size)
		return size - suffix, suffix, nil
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, SFTPError("sftp_invalid_range")
	}
	end := size - 1
	if parts[1] != "" {
		end, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || end < start {
			return 0, 0, SFTPError("sftp_invalid_range")
		}
		end = min(end, size-1)
	}
	return start, end - start + 1, nil
}
func (s *SFTPService) DownloadRange(ctx context.Context, id uint, p string, request SFTPDownloadRequest, begin func(SFTPDownload) (io.Writer, error)) error {
	p, err := remotePath(p)
	if err != nil {
		return err
	}
	return s.withClient(ctx, id, false, func(ctx context.Context, c *sftp.Client) error {
		info, err := c.Lstat(p)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return SFTPError("sftp_not_regular")
		}
		if info.Size() > MaxSFTPFileBytes {
			return SFTPError("sftp_too_large")
		}
		digest, err := hashRemoteFile(c, p, MaxSFTPFileBytes)
		if err != nil {
			return err
		}
		response := SFTPDownload{Entry: sftpEntry(p, info), Total: info.Size(), Length: info.Size(), ETag: "\"sha256:" + digest + "\""}
		if request.Range != "" && (request.IfRange == "" || request.IfRange == response.ETag) {
			response.Start, response.Length, err = sftpByteRange(request.Range, info.Size())
			if err != nil {
				return err
			}
			response.Partial = true
		}
		file, err := c.Open(p)
		if err != nil {
			return err
		}
		defer file.Close()
		opened, err := file.Stat()
		if err != nil || !sameRemoteFile(info, opened) {
			return SFTPError("sftp_conflict")
		}
		if _, err = file.Seek(response.Start, io.SeekStart); err != nil {
			return err
		}
		out, err := begin(response)
		if err != nil {
			return err
		}
		n, err := io.CopyBuffer(out, io.LimitReader(file, response.Length), make([]byte, 32<<10))
		if err != nil {
			return err
		}
		if n != response.Length {
			return SFTPError("sftp_conflict")
		}
		return ctx.Err()
	})
}
