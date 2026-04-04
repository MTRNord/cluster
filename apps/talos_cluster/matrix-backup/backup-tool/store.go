// store.go — crypto-store tarball backup and restore (persists Megolm sessions
// and sync tokens across CronJob runs via S3).
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
)

// downloadStore restores a previously uploaded crypto store tarball from S3.
// A missing store (first run) is not an error — the caller starts fresh.
func downloadStore(ctx context.Context, storeDir, s3Key string) error {
	data, err := s3Get(ctx, s3Key)
	if err != nil {
		return err
	}
	if data == nil {
		slog.Info("No existing store in S3, starting fresh", "key", s3Key)
		return nil
	}
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		return err
	}
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		target := storeDir + "/" + hdr.Name
		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0700) //nolint:errcheck
		case tar.TypeReg:
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	slog.Info("Store restored from S3", "key", s3Key, "bytes", len(data))
	return nil
}

// uploadStore tarballs the crypto store directory and uploads it to S3 for
// the next run to restore.
func uploadStore(ctx context.Context, storeDir, s3Key string) error {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := addDirToTar(tw, storeDir, "."); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("finalize tar: %w", err)
	}
	if err := gw.Close(); err != nil {
		return fmt.Errorf("finalize gzip: %w", err)
	}
	data := buf.Bytes()
	if err := s3Put(ctx, s3Key, data, "application/gzip"); err != nil {
		return err
	}
	slog.Info("Store saved to S3", "key", s3Key, "bytes", len(data))
	return nil
}

func addDirToTar(tw *tar.Writer, baseDir, arcBase string) error {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		srcPath := baseDir + "/" + e.Name()
		arcPath := arcBase + "/" + e.Name()
		if e.IsDir() {
			_ = tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeDir,
				Name:     arcPath + "/",
				Mode:     0700,
			})
			if err := addDirToTar(tw, srcPath, arcPath); err != nil {
				return err
			}
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		f, err := os.Open(srcPath)
		if err != nil {
			continue
		}
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     arcPath,
			Size:     info.Size(),
			Mode:     0600,
		}); err != nil {
			f.Close()
			return err
		}
		_, copyErr := io.Copy(tw, f)
		f.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}
