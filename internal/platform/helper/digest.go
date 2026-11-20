package helper

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// ExpectedHelperSHA256 is the hex SHA-256 of the lanflow-helper binary
// shipped with the app, set at build time:
//
//	-ldflags "-X lanflow/internal/platform/helper.ExpectedHelperSHA256=<hex>"
//
// When set, a helper binary with another digest is never run elevated.
// When empty (development builds) the digest is taken from the binary just
// before the authorization prompt, which still guarantees that the
// elevated process runs exactly the bytes the app saw (macOS: root runs a
// verified private copy; Windows: the file is held open against writers
// until the elevated process has ended).
var ExpectedHelperSHA256 string

// helperDigest returns the digest the privileged command must verify.
func helperDigest(bin string) (string, error) {
	sum, err := fileSHA256(bin)
	if err != nil {
		return "", fmt.Errorf("无法读取权限组件程序: %w", err)
	}
	return checkHelperDigest(sum)
}

// checkHelperDigest compares the helper's digest with the one recorded at
// build time, when there is one.
func checkHelperDigest(sum string) (string, error) {
	if want := strings.ToLower(strings.TrimSpace(ExpectedHelperSHA256)); want != "" {
		if sum != want {
			return "", errors.New("权限组件程序校验失败，与应用构建时记录的不一致，请重新下载安装澜流")
		}
		return want, nil
	}
	return sum, nil
}

// coreDigest returns the SHA-256 of the Mihomo binary the helper is asked
// to install, taken now, before the administrator prompt: the elevated
// installer accepts only a copy with this digest, so a file swapped while
// the user types the password is refused instead of becoming the core that
// runs as root. want, when not empty, is the digest the caller expects
// (a pinned release); another content is refused here already.
func coreDigest(bin, want string) (string, error) {
	sum, err := fileSHA256(bin)
	if err != nil {
		return "", fmt.Errorf("无法读取 Mihomo 内核: %w", err)
	}
	if want = strings.ToLower(strings.TrimSpace(want)); want != "" && sum != want {
		return "", errors.New("Mihomo 内核校验失败，与预期的摘要不一致")
	}
	return sum, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return readerSHA256(f)
}

func readerSHA256(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
