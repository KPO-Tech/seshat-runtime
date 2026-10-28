//go:build linux

package bash

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

const landlockHelperArg = "--seshat-landlock-helper"

var landlockSupport = struct {
	once sync.Once
	abi  int
}{}

func init() {
	if len(os.Args) >= 2 && os.Args[1] == landlockHelperArg {
		runLandlockHelper()
	}
	landlockAvailable()
}

func runLandlockHelper() {
	runtime.LockOSThread()
	workspaceRoot := os.Getenv("SESHAT_LANDLOCK_WORKSPACE")
	if workspaceRoot != "" {
		if err := applyLandlock(workspaceRoot); err != nil && !isLandlockUnavailable(err) {
			_, _ = fmt.Fprintf(os.Stderr, "failed to apply landlock sandbox: %v\n", err)
			os.Exit(126)
		}
	}

	if len(os.Args) < 3 {
		_, _ = fmt.Fprintln(os.Stderr, "missing landlock helper command")
		os.Exit(126)
	}
	target := os.Args[2]
	args := os.Args[2:]
	if err := unix.Exec(target, args, os.Environ()); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to exec sandbox target: %v\n", err)
		os.Exit(126)
	}
}

func landlockAvailable() bool {
	return landlockABI() > 0
}

func landlockABI() int {
	landlockSupport.once.Do(func() {
		abi, err := getLandlockABI()
		if err == nil && abi > 0 {
			landlockSupport.abi = abi
		}
	})
	return landlockSupport.abi
}

func getLandlockABI() (int, error) {
	r1, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		0,
		0,
		uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION),
	)
	if errno != 0 {
		return 0, errno
	}
	if r1 == 0 {
		return 0, unix.ENOTSUP
	}
	return int(r1), nil
}

func commandWithLandlock(shell string, args []string, workspaceRoot string) (string, []string, []string, bool) {
	if workspaceRoot == "" || !landlockAvailable() {
		return shell, args, nil, false
	}
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return shell, args, nil, false
	}
	target := shell
	if !filepath.IsAbs(target) {
		resolved, err := exec.LookPath(target)
		if err != nil {
			return shell, args, nil, false
		}
		target = resolved
	}
	helperArgs := append([]string{landlockHelperArg, target}, args...)
	return exe, helperArgs, []string{"SESHAT_LANDLOCK_WORKSPACE=" + workspaceRoot}, true
}

func applyLandlock(workspaceRoot string) error {
	abi := landlockABI()
	if abi <= 0 {
		return unix.ENOTSUP
	}

	root, err := filepath.Abs(filepath.Clean(workspaceRoot))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil { // #nosec G703 -- root is the workspace that the parent seshat process put in SESHAT_LANDLOCK_WORKSPACE when it started this helper, which only makes sure it exists
		return err
	}

	accessRO, accessRW := landlockAccess(abi)
	rulesetAttr := unix.LandlockRulesetAttr{Handled_access_fs: accessRW}
	rulesetFD, err := landlockCreateRuleset(&rulesetAttr, unsafe.Sizeof(rulesetAttr.Handled_access_fs), 0)
	if err != nil {
		return err
	}
	defer unix.Close(rulesetFD)

	if err := addLandlockPathRule(rulesetFD, "/", accessRO); err != nil {
		return err
	}
	if err := addLandlockPathRule(rulesetFD, "/dev/null", accessRW); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	if err := addLandlockPathRule(rulesetFD, root, accessRW); err != nil {
		return err
	}

	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	return landlockRestrictSelf(rulesetFD, 0)
}

func landlockAccess(abi int) (readOnly uint64, readWrite uint64) {
	readOnly = unix.LANDLOCK_ACCESS_FS_EXECUTE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR

	readWrite = readOnly |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM

	if abi >= 2 {
		readWrite |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		readWrite |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 5 {
		readWrite |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	return readOnly, readWrite
}

func addLandlockPathRule(rulesetFD int, path string, access uint64) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)

	attr := unix.LandlockPathBeneathAttr{
		Allowed_access: access,
		Parent_fd:      int32(fd),
	}
	return landlockAddRule(rulesetFD, unix.LANDLOCK_RULE_PATH_BENEATH, &attr, 0)
}

func landlockCreateRuleset(attr *unix.LandlockRulesetAttr, size uintptr, flags uint32) (int, error) {
	r1, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(attr)),
		size,
		uintptr(flags),
	)
	if errno != 0 {
		return 0, errno
	}
	return int(r1), nil
}

func landlockAddRule(rulesetFD int, ruleType int, attr *unix.LandlockPathBeneathAttr, flags uint32) error {
	_, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(rulesetFD),
		uintptr(ruleType),
		uintptr(unsafe.Pointer(attr)),
		uintptr(flags),
		0,
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func landlockRestrictSelf(rulesetFD int, flags uint32) error {
	_, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_RESTRICT_SELF,
		uintptr(rulesetFD),
		uintptr(flags),
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func isLandlockUnavailable(err error) bool {
	if err == nil {
		return false
	}
	for _, errno := range []unix.Errno{unix.ENOSYS, unix.ENOTSUP, unix.EOPNOTSUPP, unix.EINVAL, unix.EPERM} {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
}
