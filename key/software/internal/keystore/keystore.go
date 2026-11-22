// Package keystore holds the keys of the software key driver in files: one PKCS#8 DER file for each
// purpose, in a state directory that only the driver's user can reach (ADR 0011, "Software keys by default").
//
// A software key can be read by anyone who can read its volume. That is the accepted risk of the local
// installation: the volume is the boundary, so the package adds no passphrase, no import and no export.
//
// The purposes are the host's: a purpose.Set that the host declared says each purpose's algorithm, and a key is P-256 for ES256, RSA-2048
// with the public exponent 65537 for RS256 and P-384 for ES384 and ECDH-P384.
package keystore

import (
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/truvity/atoom/key/purpose"
)

// The algorithms of the key-store port (keystore.proto).
const (
	ES384       = purpose.ES384    // ECDSA over P-384 with SHA-384 (RFC 7518, section 3.4)
	ES256       = purpose.ES256    // ECDSA over P-256 with SHA-256 (RFC 7518, section 3.4)
	RS256       = purpose.RS256    // RSASSA-PKCS1-v1_5 with SHA-256 over an RSA-2048 key (RFC 7518, section 3.3)
	ECDHP384    = purpose.ECDHP384 // ECDH over P-384 (NIST SP 800-56A)
	fileExt     = ".p8"            // a PKCS#8 DER private key (RFC 5208)
	tempMark    = ".tmp-"          // the prefix of a key file that is being written
	dirMode     = 0o700            // the state directory
	fileMode    = 0o600            // a key file
	modeMask    = fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky
	maxKeyFile  = 4096
	rsaBits     = 2048
	rsaExponent = 65537
)

// curveOf is the curve of the key of an algorithm.
func curveOf(algorithm string) elliptic.Curve {
	if algorithm == ES256 {
		return elliptic.P256()
	}
	return elliptic.P384()
}

// ProblemsError is a state directory or a key file that the driver refuses. Each problem names a path and what is
// wrong with it, never a byte of a key.
type ProblemsError struct{ Problems []string }

func (e *ProblemsError) Error() string { return "state refused: " + strings.Join(e.Problems, "; ") }

// Sentinel errors of the key operations.
var (
	ErrAbsent    = errors.New("keystore: no key for the purpose")
	ErrAlgorithm = errors.New("keystore: the key has another algorithm")
	ErrPeer      = errors.New("keystore: the peer public key is not a P-384 point")
	ErrDigest    = errors.New("keystore: the digest has the wrong length for the algorithm")
)

// Store is the set of keys of one state directory. It is safe for concurrent use by one process.
type Store struct {
	dir      string
	purposes *purpose.Set
	mu       sync.Mutex
	keys     map[string]crypto.Signer // an *ecdsa.PrivateKey or, for an RS256 purpose, an *rsa.PrivateKey
}

// Check verifies the state directory and the key files in it against the host's purposes, and returns the keys it read. It writes
// nothing. The problems are paths with a reason; a directory or file that is not owned by this process's user, or that
// is not mode 0700 (directory) or 0600 (file), is a problem, and so is a key file of a purpose the host did not declare, or whose key is
// not of the algorithm the host declared.
func Check(dir string, purposes *purpose.Set) (map[string]crypto.Signer, []string) {
	var problems []string
	if p := checkDir(dir); p != "" {
		return nil, []string{p}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []string{fmt.Sprintf("%s: cannot be listed", dir)}
	}
	keys := make(map[string]crypto.Signer)
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		name, isKey := strings.CutSuffix(e.Name(), fileExt)
		switch {
		case strings.HasPrefix(e.Name(), tempMark):
			continue // a write that did not finish; Open removes it
		case !isKey:
			problems = append(problems, path+": not a key file")
			continue
		}
		decl, ok := purposes.Lookup(name)
		if !ok {
			problems = append(problems, path+": not the file of a declared purpose")
			continue
		}
		k, p := readKey(path, decl.Algorithm)
		if p != "" {
			problems = append(problems, p)
			continue
		}
		keys[name] = k
	}
	if len(problems) > 0 {
		return nil, problems
	}
	return keys, nil
}

// Open creates the state directory when it is absent (its parent must exist), removes the leftovers of an
// unfinished write, and loads the keys. It refuses a directory or a file that Check refuses.
func Open(dir string, purposes *purpose.Set) (*Store, error) {
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(dir, dirMode); err != nil {
			return nil, fmt.Errorf("keystore: create %s: %w", dir, err)
		}
		if err := os.Chmod(dir, dirMode); err != nil { // the umask may have narrowed it
			return nil, fmt.Errorf("keystore: chmod %s: %w", dir, err)
		}
	}
	keys, problems := Check(dir, purposes)
	if len(problems) > 0 {
		return nil, &ProblemsError{Problems: problems}
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), tempMark) {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	return &Store{dir: dir, purposes: purposes, keys: keys}, nil
}

// Present returns the purposes that have a key, sorted.
func (s *Store) Present() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.keys))
	for p := range s.keys {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// Ensure returns the public key of the purpose, SPKI in DER, and makes the key when it is absent. The algorithm
// must be the one of the purpose; an existing key is never replaced.
func (s *Store) Ensure(name, algorithm string) (spki []byte, created bool, err error) {
	decl, ok := s.purposes.Lookup(name)
	if !ok || algorithm != decl.Algorithm {
		return nil, false, ErrAlgorithm
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if k, ok := s.keys[name]; ok {
		spki, err = marshalPublic(k)
		return spki, false, err
	}
	k, err := generate(decl.Algorithm)
	if err != nil {
		return nil, false, fmt.Errorf("keystore: generate: %w", err)
	}
	if err := s.write(name, k); err != nil {
		return nil, false, err
	}
	s.keys[name] = k
	spki, err = marshalPublic(k)
	return spki, true, err
}

// CreatedAt is when the key of the purpose was made: the time of its file, which is written once and never replaced.
func (s *Store) CreatedAt(name string) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.keys[name]; !ok {
		return time.Time{}, ErrAbsent
	}
	fi, err := os.Stat(filepath.Join(s.dir, name+fileExt))
	if err != nil {
		return time.Time{}, fmt.Errorf("keystore: stat %s: %w", name, err)
	}
	return fi.ModTime(), nil
}

// write puts the key in a temporary file made with O_EXCL, syncs it, links it to its name (which fails when the
// name is taken, so a key is never replaced), and syncs the directory.
func (s *Store) write(name string, k crypto.Signer) error {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return fmt.Errorf("keystore: marshal: %w", err)
	}
	defer clear(der)
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, tempMark+hex.EncodeToString(rnd[:]))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode) //nolint:gosec // a path in the state directory
	if err != nil {
		return fmt.Errorf("keystore: create %s: %w", tmp, err)
	}
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(der); err != nil {
		_ = f.Close()
		return fmt.Errorf("keystore: write %s: %w", tmp, err)
	}
	if err := f.Chmod(fileMode); err != nil { // the umask cannot widen a mode, but be exact
		_ = f.Close()
		return fmt.Errorf("keystore: chmod %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("keystore: sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("keystore: close %s: %w", tmp, err)
	}
	final := filepath.Join(s.dir, name+fileExt)
	if err := os.Link(tmp, final); err != nil {
		return fmt.Errorf("keystore: place %s: %w", final, err)
	}
	d, err := os.Open(s.dir)
	if err != nil {
		return fmt.Errorf("keystore: open %s: %w", s.dir, err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("keystore: sync %s: %w", s.dir, err)
	}
	return nil
}

// PublicKey returns the public key of the purpose, SPKI in DER, or ErrAbsent.
func (s *Store) PublicKey(name string) ([]byte, error) {
	k, err := s.get(name)
	if err != nil {
		return nil, err
	}
	return marshalPublic(k)
}

// Sign signs a digest with the ES384 key of the purpose (a SHA-384 digest, 48 octets) or its ES256 key (a SHA-256
// digest, 32 octets), and returns the ASN.1 DER signature (RFC 7518, section 3.4 shows the JOSE form; the DER form
// is what the port carries, and key/signature converts it). The RS256 key signs a SHA-256 digest and returns the RSASSA-PKCS1-v1_5 signature (RFC
// 8017, section 8.2.1), 256 octets, which has no DER form. The nonce of an ECDSA signature comes from crypto/rand.
func (s *Store) Sign(name string, digest []byte) ([]byte, error) {
	k, err := s.get(name)
	if err != nil {
		return nil, err
	}
	decl, _ := s.purposes.Lookup(name)
	want := purpose.DigestLen(decl.Algorithm)
	if want == 0 {
		return nil, ErrAlgorithm
	}
	if len(digest) != want {
		return nil, ErrDigest
	}
	var opts crypto.SignerOpts // nil: an ECDSA key signs the digest as it is
	if _, ok := k.(*rsa.PrivateKey); ok {
		opts = crypto.SHA256
	}
	sig, err := k.Sign(rand.Reader, digest, opts)
	if err != nil {
		return nil, fmt.Errorf("keystore: sign: %w", err)
	}
	return sig, nil
}

// Ecdh returns the raw shared secret of the ECDH-P384 key of the purpose and a peer public key, an uncompressed
// SEC1 point (SEC 1, section 2.3.3). crypto/ecdh checks that the point is on the curve.
func (s *Store) Ecdh(name string, peer []byte) ([]byte, error) {
	k, err := s.get(name)
	if err != nil {
		return nil, err
	}
	if decl, _ := s.purposes.Lookup(name); decl.Algorithm != ECDHP384 {
		return nil, ErrAlgorithm
	}
	pub, err := ecdh.P384().NewPublicKey(peer)
	if err != nil {
		return nil, ErrPeer
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, ErrAlgorithm
	}
	priv, err := ek.ECDH()
	if err != nil {
		return nil, fmt.Errorf("keystore: ecdh key: %w", err)
	}
	secret, err := priv.ECDH(pub)
	if err != nil {
		return nil, ErrPeer
	}
	return secret, nil
}

func (s *Store) get(name string) (crypto.Signer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[name]
	if !ok {
		return nil, ErrAbsent
	}
	return k, nil
}

func marshalPublic(k crypto.Signer) ([]byte, error) {
	return x509.MarshalPKIXPublicKey(k.Public())
}

// generate makes the key of an algorithm: a P-256 key for ES256, an RSA-2048 key (the public exponent is 65537, as
// crypto/rsa fixes it) for RS256, a P-384 key for the others.
func generate(algorithm string) (crypto.Signer, error) {
	if algorithm == RS256 {
		return rsa.GenerateKey(rand.Reader, rsaBits)
	}
	return ecdsa.GenerateKey(curveOf(algorithm), rand.Reader)
}

// checkDir returns the problem of the state directory, or "".
func checkDir(dir string) string {
	fi, err := os.Lstat(dir)
	switch {
	case err != nil:
		return dir + ": cannot be read"
	case !fi.IsDir():
		return dir + ": not a directory"
	case fi.Mode()&modeMask != dirMode:
		return fmt.Sprintf("%s: mode %04o, want %04o", dir, uint32(fi.Mode()&modeMask), dirMode)
	case !ownedByMe(fi):
		return dir + ": not owned by the user of this process"
	}
	return ""
}

// readKey reads and parses one key file, after it checks the file. It returns the problem, or "". A key of
// another kind than the purpose's is refused, never replaced: a key that an earlier declaration made as a
// P-384 key for a purpose that is now ES256 must be removed by the person, because a new key changes the verifier key of everything it
// signed. The RS256 key must be RSA-2048 with the exponent 65537, the only RSA key the port knows.
func readKey(path, algorithm string) (crypto.Signer, string) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) //nolint:gosec // a path in the state directory
	if err != nil {
		return nil, path + ": cannot be opened"
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	switch {
	case err != nil:
		return nil, path + ": cannot be read"
	case !fi.Mode().IsRegular():
		return nil, path + ": not a regular file"
	case fi.Mode()&modeMask != fileMode:
		return nil, fmt.Sprintf("%s: mode %04o, want %04o", path, uint32(fi.Mode()&modeMask), fileMode)
	case !ownedByMe(fi):
		return nil, path + ": not owned by the user of this process"
	}
	der, err := io.ReadAll(io.LimitReader(f, maxKeyFile)) // a P-384 key is about 185 bytes, an RSA-2048 key about 1220
	if err != nil {
		return nil, path + ": cannot be read"
	}
	defer clear(der)
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, path + ": not a PKCS#8 private key"
	}
	if algorithm == RS256 {
		k, ok := parsed.(*rsa.PrivateKey)
		if !ok || k.N.BitLen() != rsaBits || k.E != rsaExponent {
			return nil, path + ": not an RSA-2048 key with the exponent 65537; the purpose needs one, and a key is never re-made in place"
		}
		return k, ""
	}
	curve := curveOf(algorithm)
	k, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || k.Curve != curve {
		return nil, fmt.Sprintf("%s: not a %s key; the purpose needs one, and a key is never re-made in place", path, curve.Params().Name)
	}
	return k, ""
}

func ownedByMe(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
