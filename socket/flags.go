package socket

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
)

// Flags holds the guard flags of a thin main: the same four, spelled the same, as the Rust mains have.
//
//	--allow-uid UID       a uid that may call (repeatable); without it only the uid of the process may
//	--allow-any-uid       no check of the caller's uid: the socket's file permission is the only guard
//	--socket-gid GID      the group of the socket; with it the default mode is 0660
//	--socket-mode MODE    the mode of the socket, octal (default 0600, or 0660 with --socket-gid)
type Flags struct {
	uids uidList
	any  bool
	gid  uint32
	mode string
}

// Register adds the flags to fs. Call it before fs.Parse, and [Flags.Config] after.
func (f *Flags) Register(fs *flag.FlagSet) {
	fs.Var(&f.uids, "allow-uid", "a uid that may call the socket (repeatable); without it only the uid of this process may")
	fs.BoolVar(&f.any, "allow-any-uid", false, "do not check the caller's uid: any process that can open the socket may call")
	fs.Func("socket-gid", "the group of the socket, so that its members can open it; the default mode is then 0660 (not 0)", func(s string) error {
		g, err := strconv.ParseUint(s, 10, 32)
		if err != nil || g == 0 {
			return errors.New("a group number other than 0")
		}
		f.gid = uint32(g)
		return nil
	})
	fs.StringVar(&f.mode, "socket-mode", "", "the mode of the socket, octal (default 0600, or 0660 with --socket-gid)")
}

// Config makes the configuration of the socket at path from the flags, or says what is wrong with them (the caller treats that as
// a usage error).
func (f *Flags) Config(path string) (Config, error) {
	cfg := Config{Socket: path}
	if f.any && len(f.uids) > 0 {
		return Config{}, errors.New("--allow-any-uid and --allow-uid exclude each other")
	}
	switch {
	case f.any:
		cfg.Allow = AllowAnyUID()
	case len(f.uids) > 0:
		cfg.Allow = AllowUIDs(f.uids...)
	}
	if f.gid != 0 {
		cfg.GID = f.gid
		cfg.Mode = 0o660
	}
	if f.mode != "" {
		m, err := strconv.ParseUint(f.mode, 8, 32)
		if err != nil || m > 0o777 || m == 0 {
			return Config{}, errors.New("--socket-mode is not an octal mode")
		}
		cfg.Mode = os.FileMode(m)
	}
	return cfg, nil
}

// uidList is a repeatable --allow-uid.
type uidList []uint32

func (l *uidList) String() string { return fmt.Sprint([]uint32(*l)) }

func (l *uidList) Set(s string) error {
	u, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return errors.New("a uid is a number")
	}
	*l = append(*l, uint32(u))
	return nil
}
