#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <signal.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/syscall.h>
#include <sys/uio.h>
#include <unistd.h>

/* Test-only interposer. The host harness compiles and mounts this library for
 * one disposable pacman unit. Other file descriptors use the real syscalls. */
static volatile int tripped;
static const char marker_path[] = "/run/try-omarchy-package-cut.ready";

static int target_fd(int fd) {
    const char *target = getenv("TRY_OMARCHY_CUT_TARGET");
    if (target == NULL || target[0] != '/') return 0;
    char proc_path[64], actual[PATH_MAX + 1];
    int n = snprintf(proc_path, sizeof(proc_path), "/proc/self/fd/%d", fd);
    if (n <= 0 || (size_t)n >= sizeof(proc_path)) return 0;
    ssize_t length = syscall(SYS_readlink, proc_path, actual, sizeof(actual) - 1);
    if (length <= 0 || (size_t)length >= sizeof(actual)) return 0;
    actual[length] = '\0';
    return strcmp(actual, target) == 0;
}

static int valid_nonce(const char *nonce) {
    if (nonce == NULL || strlen(nonce) != 32) return 0;
    for (int i = 0; i < 32; ++i) {
        if (!((nonce[i] >= '0' && nonce[i] <= '9') ||
              (nonce[i] >= 'a' && nonce[i] <= 'f'))) return 0;
    }
    return 1;
}

static void stop_after_partial_write(int payload_fd, ssize_t written) {
    const char *nonce = getenv("TRY_OMARCHY_CUT_NONCE");
    if (written <= 0 || !valid_nonce(nonce) || syscall(SYS_fsync, payload_fd) != 0)
        _exit(121);
    int marker = syscall(SYS_openat, AT_FDCWD, marker_path,
                         O_CREAT | O_EXCL | O_WRONLY | O_CLOEXEC, 0644);
    if (marker < 0) _exit(122);
    char line[80];
    int size = snprintf(line, sizeof(line), "%s:%zd\n", nonce, written);
    if (size <= 0 || (size_t)size >= sizeof(line) ||
        syscall(SYS_write, marker, line, (size_t)size) != size ||
        syscall(SYS_fsync, marker) != 0) _exit(123);
    syscall(SYS_close, marker);
    if (syscall(SYS_kill, syscall(SYS_getpid), SIGSTOP) != 0) _exit(124);
    _exit(125); /* A later SIGCONT must not complete the transaction. */
}

ssize_t write(int fd, const void *buf, size_t count) {
    if (count == 0 || !target_fd(fd) || !__sync_bool_compare_and_swap(&tripped, 0, 1))
        return syscall(SYS_write, fd, buf, count);
    size_t first = count < 4096 ? count : 4096;
    ssize_t written = syscall(SYS_write, fd, buf, first);
    if (written <= 0) {
        tripped = 0;
        return written;
    }
    stop_after_partial_write(fd, written);
    return written;
}

ssize_t writev(int fd, const struct iovec *iov, int iovcnt) {
    if (iovcnt <= 0 || !target_fd(fd) || !__sync_bool_compare_and_swap(&tripped, 0, 1))
        return syscall(SYS_writev, fd, iov, iovcnt);
    for (int i = 0; i < iovcnt; ++i) {
        if (iov[i].iov_len == 0) continue;
        struct iovec first = {iov[i].iov_base, iov[i].iov_len < 4096 ? iov[i].iov_len : 4096};
        ssize_t written = syscall(SYS_writev, fd, &first, 1);
        if (written <= 0) {
            tripped = 0;
            return written;
        }
        stop_after_partial_write(fd, written);
        return written;
    }
    tripped = 0;
    return syscall(SYS_writev, fd, iov, iovcnt);
}

ssize_t pwrite(int fd, const void *buf, size_t count, off_t offset) {
    if (count == 0 || !target_fd(fd) || !__sync_bool_compare_and_swap(&tripped, 0, 1))
        return syscall(SYS_pwrite64, fd, buf, count, offset);
    size_t first = count < 4096 ? count : 4096;
    ssize_t written = syscall(SYS_pwrite64, fd, buf, first, offset);
    if (written <= 0) {
        tripped = 0;
        return written;
    }
    stop_after_partial_write(fd, written);
    return written;
}
