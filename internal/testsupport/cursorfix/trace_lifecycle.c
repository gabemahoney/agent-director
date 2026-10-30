//go:build ignore

// Linux tracee fixture: libc threads make nonleader exec explicit, without Go
// runtime threads or a second process supervisor. Commands synchronize on stdin.
#define _GNU_SOURCE
#include <pthread.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/syscall.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>

static const char *self;

static void *exec_thread(void *unused) {
    (void)unused;
    printf("exec-thread %d %ld\n", getpid(), syscall(SYS_gettid));
    if (getchar() != 'x') _exit(41);
    execl(self, self, "after-exec", NULL);
    _exit(42);
}

int main(int argc, char **argv) {
    if (argc != 2) return 43;
    self = argv[0];
    setbuf(stdout, NULL);
    if (!strcmp(argv[1], "leaf")) return 17;
    if (!strcmp(argv[1], "held-child")) for (;;) pause();
    if (!strcmp(argv[1], "nonleader")) {
        pthread_t thread;
        if (pthread_create(&thread, NULL, exec_thread, NULL)) return 44;
        for (;;) pause();
    }
    if (!strcmp(argv[1], "after-exec")) {
        printf("after-exec %d %ld\n", getpid(), syscall(SYS_gettid));
        return getchar() == 'q' ? 0 : 45;
    }
    if (!strcmp(argv[1], "burst")) {
        if (getchar() != 'b') return 46;
        pid_t children[16];
        for (int i = 0; i < 16; ++i) {
            children[i] = i % 2 ? vfork() : fork();
            if (children[i] < 0) return 47;
            if (!children[i]) {
                // Ordinary children without ACP endpoints stay owned.
                close(STDIN_FILENO);
                close(STDOUT_FILENO);
                execl(self, self, "leaf", NULL);
                _exit(48);
            }
        }
        for (int i = 0; i < 16; ++i) {
            int status;
            if (waitpid(children[i], &status, 0) != children[i] ||
                !WIFEXITED(status) || WEXITSTATUS(status) != 17) return 49;
        }
        puts("waited 16 exit=17");
        return getchar() == 'q' ? 0 : 50;
    }
    if (!strcmp(argv[1], "fork-stop")) {
        puts("fork-ready");
        if (getchar() != 'f') return 51;
        pid_t child = fork();
        if (child < 0) return 52;
        if (!child) {
            close(STDIN_FILENO);
            close(STDOUT_FILENO);
        }
        for (;;) pause();
    }
    if (!strcmp(argv[1], "exec-child")) {
        puts("fork-ready");
        if (getchar() != 'f') return 54;
        pid_t child = fork();
        if (child < 0) return 55;
        if (!child) {
            close(STDIN_FILENO);
            close(STDOUT_FILENO);
            execl(self, self, "held-child", NULL);
            _exit(56);
        }
        if (getchar() != 'k' || kill(child, SIGKILL)) return 57;
        puts("kill-sent");
        int status;
        if (waitpid(child, &status, 0) != child ||
            !WIFSIGNALED(status) || WTERMSIG(status) != SIGKILL) return 58;
        puts("waited child signal=9");
        if (getchar() != 'p') return 59;
        puts("responsive");
        return getchar() == 'q' ? 0 : 60;
    }
    return 53;
}
