#define SYS_read 0
#define SYS_write 1
#define SYS_close 3
#define SYS_clone 56
#define SYS_execve 59
#define SYS_wait4 61
#define SYS_getppid 110
#define SYS_setresuid 117
#define SYS_setresgid 119
#define SYS_unshare 272
#define SYS_pipe2 293
#define SYS_exit_group 231
#define CLONE_NEWUSER 0x10000000
#define SIGCHLD 17
#define O_CLOEXEC 02000000

static long sys6(long n, long a, long b, long c, long d, long e, long f) {
  long ret;
  register long r10 __asm__("r10") = d;
  register long r8 __asm__("r8") = e;
  register long r9 __asm__("r9") = f;
  __asm__ volatile("syscall"
                   : "=a"(ret)
                   : "a"(n), "D"(a), "S"(b), "d"(c), "r"(r10), "r"(r8), "r"(r9)
                   : "rcx", "r11", "memory");
  return ret;
}

#define sys3(n, a, b, c) sys6((n), (long)(a), (long)(b), (long)(c), 0, 0, 0)

static unsigned long slen(const char *s) {
  unsigned long n = 0;
  while (s[n]) n++;
  return n;
}

static void say(const char *s) { sys3(SYS_write, 2, s, slen(s)); }

static __attribute__((noreturn)) void die(const char *what, long err) {
  char num[24];
  int i = 23;
  unsigned long v = err < 0 ? (unsigned long)-err : (unsigned long)err;
  num[i] = 0;
  do {
    num[--i] = (char)('0' + v % 10);
    v /= 10;
  } while (v && i > 0);
  say("swc-userns-stage: ");
  say(what);
  say(" errno ");
  say(num + i);
  say("\n");
  sys3(SYS_exit_group, 125, 0, 0);
  __builtin_unreachable();
}

static void utoa(unsigned long v, char *out) {
  char tmp[24];
  int i = 0;
  do {
    tmp[i++] = (char)('0' + v % 10);
    v /= 10;
  } while (v);
  while (i) *out++ = tmp[--i];
  *out = 0;
}

static long atou(const char *s) {
  long v = 0;
  if (!*s) return -1;
  for (; *s; s++) {
    if (*s < '0' || *s > '9') return -1;
    v = v * 10 + (*s - '0');
  }
  return v;
}

static long spawn_wait(char **argv, char **envp) {
  long pid = sys6(SYS_clone, SIGCHLD, 0, 0, 0, 0, 0);
  if (pid < 0) return pid;
  if (pid == 0) {
    sys3(SYS_execve, argv[0], argv, envp);
    sys3(SYS_exit_group, 127, 0, 0);
  }
  int status = 0;
  long w = sys6(SYS_wait4, pid, (long)&status, 0, 0, 0, 0);
  if (w < 0) return w;
  return (status & 0x7f) == 0 && ((status >> 8) & 0xff) == 0 ? 0 : -1000 - status;
}

static __attribute__((noreturn)) void helper(int rfd, char *ppid, char *uidb, char *gidb, char *count, char **envp) {
  char c;
  if (sys3(SYS_read, rfd, &c, 1) != 1) sys3(SYS_exit_group, 3, 0, 0);
  char *uargv[] = {"/usr/bin/newuidmap", ppid, "0", uidb, count, 0};
  if (spawn_wait(uargv, envp) != 0) sys3(SYS_exit_group, 4, 0, 0);
  char *gargv[] = {"/usr/bin/newgidmap", ppid, "0", gidb, count, 0};
  if (spawn_wait(gargv, envp) != 0) sys3(SYS_exit_group, 5, 0, 0);
  sys3(SYS_exit_group, 0, 0, 0);
  __builtin_unreachable();
}

__attribute__((used)) void stage_main(long *sp) {
  long argc = sp[0];
  char **argv = (char **)(sp + 1);
  char **envp = argv + argc + 1;
  if (argc < 6) {
    say("usage: swc-userns-stage <outer-uid> <outer-gid> <count> <inner-id> <program> [args...]\n");
    sys3(SYS_exit_group, 2, 0, 0);
  }
  long count = atou(argv[3]);
  long inner = atou(argv[4]);
  if (atou(argv[1]) < 0 || atou(argv[2]) < 0 || count <= 0 || inner < 0 || inner >= count) {
    say("swc-userns-stage: ids must be decimal and inner-id below count\n");
    sys3(SYS_exit_group, 2, 0, 0);
  }
  int fds[2];
  long r = sys3(SYS_pipe2, fds, O_CLOEXEC, 0);
  if (r < 0) die("pipe2", r);
  char ppid[24];
  long pid = sys6(SYS_clone, SIGCHLD, 0, 0, 0, 0, 0);
  if (pid < 0) die("clone helper", pid);
  if (pid == 0) {
    sys3(SYS_close, fds[1], 0, 0);
    utoa((unsigned long)sys3(SYS_getppid, 0, 0, 0), ppid);
    helper(fds[0], ppid, argv[1], argv[2], argv[3], envp);
  }
  sys3(SYS_close, fds[0], 0, 0);
  r = sys3(SYS_unshare, CLONE_NEWUSER, 0, 0);
  if (r < 0) die("unshare(CLONE_NEWUSER)", r);
  if (sys3(SYS_write, fds[1], "x", 1) != 1) die("signal helper", 0);
  sys3(SYS_close, fds[1], 0, 0);
  int status = 0;
  r = sys6(SYS_wait4, pid, (long)&status, 0, 0, 0, 0);
  if (r < 0) die("wait helper", r);
  if (status != 0) die("newuidmap/newgidmap helper status", status);
  r = sys3(SYS_setresgid, inner, inner, inner);
  if (r < 0) die("setresgid", r);
  r = sys3(SYS_setresuid, inner, inner, inner);
  if (r < 0) die("setresuid", r);
  r = sys3(SYS_execve, argv[5], argv + 5, envp);
  die("execve", r);
}

__attribute__((naked, noreturn)) void _start(void) {
  __asm__ volatile(
      "xor %rbp, %rbp\n"
      "mov %rsp, %rdi\n"
      "and $-16, %rsp\n"
      "call stage_main\n"
      "hlt\n");
}
