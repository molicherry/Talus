package service

import (
	"archive/zip"
	"context"
	"github.com/pkg/sftp"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

type sftpArchiveEntry struct {
	path, name string
	info       os.FileInfo
}

func (s *SFTPService) Archive(ctx context.Context, id uint, p string, begin func(string) (io.Writer, error)) error {
	p, err := remotePath(p)
	if err != nil {
		return err
	}
	return s.withClient(ctx, id, false, func(ctx context.Context, c *sftp.Client) error {
		info, err := c.Lstat(p)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return SFTPError("sftp_not_directory")
		}
		root, err := c.RealPath(p)
		if err != nil {
			return err
		}
		name := path.Base(root)
		if name == "/" || name == "." {
			name = "files"
		}
		manifest := []sftpArchiveEntry{{root, name + "/", info}}
		var total int64
		var metadataBytes int64
		setSFTPWireBudget(ctx, sftpDirectoryWireLimit)
		var scan func(string, string, int) error
		scan = func(remote, relative string, depth int) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if depth > 32 {
				return SFTPError("sftp_directory_too_large")
			}
			entries, err := c.ReadDirContext(ctx, remote)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if entry.Name() == "." || entry.Name() == ".." {
					continue
				}
				if !utf8.ValidString(entry.Name()) || strings.ContainsAny(entry.Name(), "/\\\x00") {
					return SFTPError("sftp_invalid_path")
				}
				if entry.Mode()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Mode().IsRegular()) {
					continue
				}
				if len(manifest) >= sftpEntryLimit {
					return SFTPError("sftp_directory_too_large")
				}
				if entry.Size() < 0 {
					return SFTPError("sftp_failed")
				}
				next := path.Join(remote, entry.Name())
				if len(next) > 4096 {
					return SFTPError("sftp_invalid_path")
				}
				archivePath := path.Join(relative, entry.Name())
				if entry.IsDir() {
					archivePath += "/"
				} else {
					if entry.Size() > MaxSFTPFileBytes-total {
						return SFTPError("sftp_too_large")
					}
					total += entry.Size()
				}
				metadataBytes += int64(2*len(archivePath) + 256)
				if metadataBytes > 2<<20 {
					return SFTPError("sftp_directory_too_large")
				}
				manifest = append(manifest, sftpArchiveEntry{next, archivePath, entry})
				if entry.IsDir() {
					if err := scan(next, archivePath, depth+1); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err = scan(root, name, 0); err != nil {
			return err
		}
		setSFTPWireBudget(ctx, -1)
		writer, err := begin(name + ".zip")
		if err != nil {
			return err
		}
		archive := zip.NewWriter(writer)
		for _, entry := range manifest {
			if err := ctx.Err(); err != nil {
				return err
			}
			current, err := c.Lstat(entry.path)
			if err != nil {
				return err
			}
			if !sameRemoteFile(entry.info, current) {
				return SFTPError("sftp_conflict")
			}
			header, err := zip.FileInfoHeader(current)
			if err != nil {
				return err
			}
			header.Name = entry.name
			header.Method = zip.Store
			out, err := archive.CreateHeader(header)
			if err != nil {
				return err
			}
			if current.IsDir() {
				continue
			}
			file, err := c.Open(entry.path)
			if err != nil {
				return err
			}
			opened, err := file.Stat()
			if err != nil || !sameRemoteFile(current, opened) {
				file.Close()
				return SFTPError("sftp_conflict")
			}
			n, copyErr := io.CopyBuffer(out, io.LimitReader(file, current.Size()), make([]byte, 32<<10))
			after, statErr := file.Stat()
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if statErr != nil {
				return statErr
			}
			if closeErr != nil {
				return closeErr
			}
			if n != current.Size() || !sameRemoteFile(current, after) {
				return SFTPError("sftp_conflict")
			}
		}
		return archive.Close()
	})
}
