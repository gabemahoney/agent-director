// Linux synthetic-fixture pipe observation: retain bytes while the peer stops reading.
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

int main(int argc, char **argv) {
    if (argc != 6) return 2;
    char path[128];
    snprintf(path, sizeof(path), "/proc/%s/fd/0", argv[1]);
    int input = open(path, O_RDONLY | O_NONBLOCK);
    if (input < 0 || fcntl(input, F_SETPIPE_SZ, 4096) < 0) return 3;
    FILE *ready = fopen(argv[2], "w");
    if (!ready) return 4;
    fclose(ready);
    while (access(argv[3], F_OK) != 0) usleep(10000);
    int pressure = open(path, O_WRONLY | O_NONBLOCK);
    char padding[4096];
    memset(padding, 'x', sizeof(padding));
    if (pressure < 0 || write(pressure, padding, sizeof(padding)) != sizeof(padding)) return 9;
    close(pressure);
    char filled[4096];
    snprintf(filled, sizeof(filled), "%s.done", argv[3]);
    FILE *barrier = fopen(filled, "w");
    if (!barrier) return 10;
    fclose(barrier);
    while (access(argv[4], F_OK) != 0) usleep(10000);
    int output = open(argv[5], O_WRONLY | O_CREAT | O_EXCL, 0600);
    if (output < 0) return 5;
    char buffer[4096];
    for (;;) {
        ssize_t n = read(input, buffer, sizeof(buffer));
        if (n == 0) break;
        if (n < 0) { if (errno == EAGAIN || errno == EINTR) { usleep(10000); continue; } return 6; }
        ssize_t done = 0;
        while (done < n) { ssize_t w = write(output, buffer + done, n - done); if (w <= 0) return 7; done += w; }
    }
    close(input);
    return close(output) == 0 ? 0 : 8;
}
