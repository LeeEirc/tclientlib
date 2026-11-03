#!/usr/bin/env python3
"""Black-box PTY/TCP regression tests for tclient (Python standard library only).

Usage: go build -o /tmp/tclient ./cmd/tclient
       python3 scripts/test_cli_pty.py /tmp/tclient

Every server binds only to loopback and all child processes and sockets are
closed on failure. PTYs verify the real terminal behavior that pipe-only tests
cannot exercise. Linux is the CI target; /dev/full is the only Linux-specific
scenario and is skipped when the device is unavailable.
"""

import argparse
import errno
import fcntl
import json
import os
import pty
import select
import signal
import socket
import struct
import subprocess
import tempfile
import termios
import threading
import time


IAC = b"\xff"
WILL = b"\xfb"
WONT = b"\xfc"
DO = b"\xfd"
DONT = b"\xfe"
ECHO = b"\x01"
NAWS = b"\x1f"


def eventually(predicate, message, timeout=4.0):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = predicate()
        if result:
            return result
        time.sleep(0.01)
    raise AssertionError(message)


class Peer:
    def __init__(self):
        self.listener = socket.socket()
        self.listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.listener.bind(("127.0.0.1", 0))
        self.listener.listen(1)
        self.listener.settimeout(0.1)
        self.port = self.listener.getsockname()[1]
        self.conn = None
        self.received = bytearray()
        self.eof = threading.Event()
        self.connected = threading.Event()
        self.errors = []
        self.stopped = threading.Event()
        self.thread = threading.Thread(target=self._receive, daemon=True)
        self.thread.start()

    def _receive(self):
        try:
            while not self.stopped.is_set():
                try:
                    self.conn, _ = self.listener.accept()
                    break
                except socket.timeout:
                    continue
            if self.conn is None:
                return
            self.conn.settimeout(0.1)
            self.connected.set()
            while not self.stopped.is_set():
                try:
                    data = self.conn.recv(4096)
                except socket.timeout:
                    continue
                if not data:
                    self.eof.set()
                    return
                self.received.extend(data)
        except OSError as exc:
            if not self.stopped.is_set():
                self.errors.append(str(exc))

    def send(self, data):
        assert self.connected.wait(4), "client did not connect"
        self.conn.sendall(data)

    def wait_bytes(self, data, timeout=4):
        return eventually(lambda: data in bytes(self.received),
                          f"missing wire bytes {data!r}, received {bytes(self.received)!r}", timeout)

    def close_remote(self):
        assert self.connected.wait(4)
        self.conn.shutdown(socket.SHUT_WR)

    def close(self):
        self.stopped.set()
        if self.conn:
            try:
                self.conn.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            self.conn.close()
        self.listener.close()
        self.thread.join(timeout=2)
        assert not self.thread.is_alive(), "server thread did not terminate"


class TTYClient:
    def __init__(self, binary, port, extra=(), stdout=None, nonblocking=False):
        self.master, self.slave = pty.openpty()
        fcntl.ioctl(self.slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
        if nonblocking:
            flags = fcntl.fcntl(self.slave, fcntl.F_GETFL)
            fcntl.fcntl(self.slave, fcntl.F_SETFL, flags | os.O_NONBLOCK)
        self.original = termios.tcgetattr(self.slave)
        self.original_flags = fcntl.fcntl(self.slave, fcntl.F_GETFL)
        self.output = bytearray()
        self.process = subprocess.Popen(
            [binary, *extra, "127.0.0.1", str(port)], stdin=self.slave,
            stdout=self.slave if stdout is None else stdout,
            stderr=self.slave, start_new_session=True)

    def read(self):
        while select.select([self.master], [], [], 0)[0]:
            try:
                data = os.read(self.master, 65536)
            except OSError as exc:
                if exc.errno == errno.EIO:
                    break
                raise
            if not data:
                break
            self.output.extend(data)
        return bytes(self.output)

    def wait_output(self, text):
        return eventually(lambda: text in self.read(),
                          f"missing output {text!r}; got {self.read()!r}")

    def send(self, data):
        os.write(self.master, data)

    def wait_raw(self):
        eventually(lambda: not termios.tcgetattr(self.slave)[3] & termios.ICANON,
                   "client never entered raw/noncanonical mode")

    def wait_exit(self, code=None):
        result = self.process.wait(timeout=5)
        self.read()
        assert termios.tcgetattr(self.slave) == self.original, "terminal was not restored"
        assert fcntl.fcntl(self.slave, fcntl.F_GETFL) == self.original_flags, "terminal file flags were not restored"
        if code is not None:
            assert result == code, f"exit={result}, expected={code}; output={self.output!r}"
        return result

    def quit(self):
        self.send(b"\x1dquit\r")
        return self.wait_exit(0)

    def close(self):
        if self.process.poll() is None:
            self.process.kill()
            self.process.wait()
        for stream in (self.process.stdout, self.process.stderr):
            if stream is not None:
                stream.close()
        os.close(self.master)
        os.close(self.slave)


def run_case(name, fn, results):
    start = time.monotonic()
    try:
        detail = fn()
        results.append({"case": name, "ok": True, "seconds": round(time.monotonic() - start, 3),
                        "detail": detail})
    except Exception as exc:
        results.append({"case": name, "ok": False, "seconds": round(time.monotonic() - start, 3),
                        "error": f"{type(exc).__name__}: {exc}"})
    print(json.dumps(results[-1], ensure_ascii=False), flush=True)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("binary")
    parser.add_argument("--out", help="optional JSON results file")
    args = parser.parse_args()
    binary = os.path.abspath(args.binary)
    results = []

    def failed_dial(nonblocking=False):
        reserve = socket.socket()
        reserve.bind(("127.0.0.1", 0))
        port = reserve.getsockname()[1]
        child = TTYClient(binary, port, ["--timeout", "100ms"], nonblocking=nonblocking)
        try:
            code = child.wait_exit()
            assert code != 0, "connection failure returned success"
            return {"exit": code}
        finally:
            child.close()
            reserve.close()

    def remote_eof():
        peer = Peer()
        child = TTYClient(binary, peer.port)
        try:
            peer.send(b"REMOTE OUTPUT\r\n")
            child.wait_raw()
            child.wait_output(b"REMOTE OUTPUT")
            peer.close_remote()
            code = child.wait_exit(0)
            return {"exit": code}
        finally:
            child.close()
            peer.close()

    def escape_quit(nonblocking=False):
        peer = Peer()
        child = TTYClient(binary, peer.port, nonblocking=nonblocking)
        try:
            peer.send(b"READY\r\n")
            child.wait_output(b"READY")
            child.wait_raw()
            child.quit()
            assert peer.eof.wait(3), "remote socket did not close"
            assert bytes(peer.received) == b"", f"local command leaked: {bytes(peer.received)!r}"
            return {"wire_hex": bytes(peer.received).hex()}
        finally:
            child.close()
            peer.close()

    def terminated():
        peer = Peer()
        child = TTYClient(binary, peer.port)
        try:
            peer.send(b"READY\r\n")
            child.wait_raw()
            child.process.send_signal(signal.SIGTERM)
            code = child.wait_exit()
            assert code != 0, "signal interruption returned success"
            return {"exit": code}
        finally:
            child.close()
            peer.close()

    def naws_resize():
        peer = Peer()
        child = TTYClient(binary, peer.port)
        try:
            peer.send(IAC + DO + NAWS + b"READY\r\n")
            peer.wait_bytes(IAC + WILL + NAWS)
            peer.wait_bytes(IAC + b"\xfa" + NAWS + b"\x00\x50\x00\x18" + IAC + b"\xf0")
            child.wait_raw()
            fcntl.ioctl(child.slave, termios.TIOCSWINSZ, struct.pack("HHHH", 37, 111, 0, 0))
            child.process.send_signal(signal.SIGWINCH)
            peer.wait_bytes(IAC + b"\xfa" + NAWS + b"\x00\x6f\x00\x25" + IAC + b"\xf0")
            child.quit()
            return {"wire_hex": bytes(peer.received).hex()}
        finally:
            child.close()
            peer.close()

    def echo_transitions():
        peer = Peer()
        child = TTYClient(binary, peer.port)
        try:
            peer.send(IAC + WILL + ECHO + b"REMOTE-ECHO\r\n")
            peer.wait_bytes(IAC + DO + ECHO)
            child.wait_output(b"REMOTE-ECHO")
            child.wait_raw()
            child.send(b"abc")
            peer.wait_bytes(b"abc")
            peer.send(IAC + WONT + ECHO + b"LOCAL-ECHO\r\n")
            peer.wait_bytes(IAC + DONT + ECHO)
            child.wait_output(b"LOCAL-ECHO")
            child.send(b"xyz")
            peer.wait_bytes(b"xyz")
            child.wait_output(b"xyz")
            child.quit()
            assert b"abc" not in child.read(), f"remote echo duplicated locally: {child.read()!r}"
            return {"wire_hex": bytes(peer.received).hex()}
        finally:
            child.close()
            peer.close()

    def login_resize_refresh():
        peer = Peer()
        child = TTYClient(binary, peer.port, ["--auto-login", "--user", "admin"])
        initial = IAC + b"\xfa" + NAWS + b"\x00\x50\x00\x18" + IAC + b"\xf0"
        resized = IAC + b"\xfa" + NAWS + b"\x00\x6f\x00\x25" + IAC + b"\xf0"
        try:
            peer.send(IAC + DO + NAWS + b"Login:")
            peer.wait_bytes(initial)
            peer.wait_bytes(b"admin\r\n")
            # Login is still waiting for the shell prompt. No second resize
            # signal is sent after login: the client must refresh on subscribe.
            fcntl.ioctl(child.slave, termios.TIOCSWINSZ, struct.pack("HHHH", 37, 111, 0, 0))
            child.process.send_signal(signal.SIGWINCH)
            peer.send(b"\r\nrouter#\r\nSESSION READY\r\n")
            child.wait_output(b"SESSION READY")
            child.wait_raw()
            peer.wait_bytes(resized)
            child.quit()
            return {"wire_hex": bytes(peer.received).hex(), "refreshed_after_login": True}
        finally:
            child.close()
            peer.close()

    def split_newlines():
        peer = Peer()
        child = TTYClient(binary, peer.port, ["--echo", "off"])
        try:
            peer.send(b"READY\r\n")
            child.wait_output(b"READY")
            child.wait_raw()
            child.send(b"first\r")
            peer.wait_bytes(b"first\r\n")
            child.send(b"\nsecond\r")
            peer.wait_bytes(b"second\r\n")
            child.send(b"\x00third\n")
            peer.wait_bytes(b"third\r\n")
            child.quit()
            want = b"first\r\nsecond\r\nthird\r\n"
            assert bytes(peer.received) == want, f"wire={bytes(peer.received)!r}, want={want!r}"
            return {"wire_hex": bytes(peer.received).hex()}
        finally:
            child.close()
            peer.close()

    def pipe_half_close():
        peer = Peer()
        child = subprocess.Popen([binary, "--close-write", "127.0.0.1", str(peer.port)], stdin=subprocess.PIPE,
                                 stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        data = b"show\r\nraw\xffbyte\r\x00tail\n"
        try:
            assert peer.connected.wait(3)
            child.stdin.write(data)
            child.stdin.close()
            assert peer.eof.wait(3), "stdin EOF did not half-close TCP write"
            expected = data.replace(b"\xff", b"\xff\xff")
            assert bytes(peer.received) == expected, f"pipe input changed: {bytes(peer.received)!r}"
            peer.send(b"\r\x00FINAL RESULT\r\n")
            peer.close_remote()
            code = child.wait(timeout=5)
            output = child.stdout.read()
            error = child.stderr.read()
            assert code == 0, f"exit={code} stderr={error!r}"
            assert output == b"\rFINAL RESULT\r\n", f"final output lost/changed: {output!r}"
            return {"wire_hex": bytes(peer.received).hex(), "stdout_hex": output.hex(), "exit": code}
        finally:
            if child.poll() is None:
                child.kill()
                child.wait()
            peer.close()

    def pipe_eof_keeps_negotiation():
        peer = Peer()
        child = subprocess.Popen([binary, "127.0.0.1", str(peer.port)], stdin=subprocess.PIPE,
                                 stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            assert peer.connected.wait(3)
            child.stdin.write(b"show status\n")
            child.stdin.close()
            peer.wait_bytes(b"show status\n")
            # An EOF notification is never expected in default mode. Waiting
            # on the event allows the child to consume the input EOF before
            # the server starts a later protocol negotiation.
            assert not peer.eof.wait(0.2), "default stdin EOF closed the Telnet write direction"
            peer.send(IAC + WILL + ECHO + b"FIRST OUTPUT\r\n")
            peer.wait_bytes(IAC + DO + ECHO)
            peer.send(b"FINAL OUTPUT\r\n")
            peer.close_remote()
            code = child.wait(timeout=5)
            output = child.stdout.read()
            error = child.stderr.read()
            assert code == 0, f"exit={code} stderr={error!r}"
            assert output == b"FIRST OUTPUT\r\nFINAL OUTPUT\r\n", f"output lost: {output!r}"
            return {"wire_hex": bytes(peer.received).hex(), "stdout_hex": output.hex(), "exit": code}
        finally:
            if child.poll() is None:
                child.kill()
                child.wait()
            peer.close()

    def password_signal():
        peer = Peer()
        child = TTYClient(binary, peer.port, ["--auto-login", "--user", "admin", "--ask-pass"])
        try:
            eventually(lambda: b"assword" in child.read(), "no password prompt")
            child.process.send_signal(signal.SIGTERM)
            code = child.wait_exit()
            assert code != 0, "password signal interruption returned success"
            return {"exit": code}
        finally:
            child.close()
            peer.close()

    def trace_permissions():
        peer = Peer()
        with tempfile.TemporaryDirectory(prefix="tclient-trace-") as folder:
            path = os.path.join(folder, "session.trace")
            child = TTYClient(binary, peer.port, ["--trace-file", path])
            try:
                peer.send(b"READY\r\n")
                child.wait_output(b"READY")
                child.wait_raw()
                child.quit()
                mode = os.stat(path).st_mode & 0o777
                assert mode == 0o600, f"trace mode={oct(mode)}"
                before = open(path, "rb").read()
                refused = subprocess.run(
                    [binary, "--trace-file", path, "127.0.0.1", str(peer.port)],
                    input=b"", stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=5)
                assert refused.returncode != 0, "existing trace was overwritten"
                after = open(path, "rb").read()
                assert after == before, "existing trace content was changed"
                return {"mode": oct(mode), "existing_file_exit": refused.returncode}
            finally:
                child.close()
                peer.close()

    def stdout_error():
        if not os.path.exists("/dev/full"):
            return {"skipped": "/dev/full is unavailable on this platform"}
        peer = Peer()
        with open("/dev/full", "wb") as output:
            child = subprocess.Popen([binary, "127.0.0.1", str(peer.port)],
                                     stdin=subprocess.DEVNULL, stdout=output, stderr=subprocess.PIPE)
            try:
                peer.send(b"OUTPUT CANNOT BE WRITTEN\r\n")
                peer.close_remote()
                code = child.wait(timeout=5)
                error = child.stderr.read()
                assert code != 0, f"stdout write error returned success: {error!r}"
                return {"exit": code, "stderr": error.decode(errors="replace")}
            finally:
                if child.poll() is None:
                    child.kill()
                    child.wait()
                peer.close()

    def broken_stdout_pipe():
        peer = Peer()
        child = TTYClient(binary, peer.port, stdout=subprocess.PIPE)
        try:
            assert peer.connected.wait(3)
            child.wait_raw()
            child.process.stdout.close()
            peer.send(b"OUTPUT TO CLOSED PIPE\r\n")
            code = child.wait_exit()
            assert code > 0, f"broken pipe exit={code}; likely exited directly from SIGPIPE"
            return {"exit": code}
        finally:
            child.close()
            peer.close()

    def redirected_stdout():
        peer = Peer()
        with tempfile.TemporaryFile() as output:
            child = TTYClient(binary, peer.port, stdout=output)
            try:
                peer.send(b"REMOTE DATA ONLY\r\n")
                child.wait_raw()
                eventually(lambda: os.fstat(output.fileno()).st_size > 0,
                           "remote output did not reach redirected stdout")
                child.send(b"x")
                peer.wait_bytes(b"x")
                child.wait_output(b"x")
                child.quit()
                assert b"tclient> " in child.read(), "local commands were not shown on stderr"
                output.seek(0)
                data = output.read()
                assert data == b"REMOTE DATA ONLY\r\n", f"local UI polluted stdout: {data!r}"
                return {"stdout_hex": data.hex()}
            finally:
                child.close()
                peer.close()

    def fill_stdout_pipe(peer, child):
        assert peer.connected.wait(3)
        child.wait_raw()
        pipe_fd = child.process.stdout.fileno()
        capacity = fcntl.fcntl(pipe_fd, fcntl.F_GETPIPE_SZ)
        peer.send(b"x" * (capacity + 65536))
        eventually(lambda: struct.unpack("i", fcntl.ioctl(
            pipe_fd, termios.FIONREAD, struct.pack("i", 0)))[0] >= capacity,
            "remote output did not fill stdout pipe")
        return capacity

    def blocked_stdout_signal():
        if not hasattr(fcntl, "F_GETPIPE_SZ"):
            return {"skipped": "Linux pipe capacity ioctl is unavailable"}
        peer = Peer()
        child = TTYClient(binary, peer.port, stdout=subprocess.PIPE)
        try:
            capacity = fill_stdout_pipe(peer, child)
            child.process.send_signal(signal.SIGTERM)
            code = child.wait_exit()
            assert code > 0, f"blocked-output signal exit={code}"
            return {"exit": code, "pipe_capacity": capacity}
        finally:
            child.close()
            peer.close()

    def blocked_stdout_quit():
        if not hasattr(fcntl, "F_GETPIPE_SZ"):
            return {"skipped": "Linux pipe capacity ioctl is unavailable"}
        peer = Peer()
        child = TTYClient(binary, peer.port, stdout=subprocess.PIPE)
        try:
            capacity = fill_stdout_pipe(peer, child)
            child.quit()
            assert b"tclient> " in child.read(), "local prompt could not bypass blocked stdout"
            assert bytes(peer.received) == b"", "local quit was sent to the remote peer"
            return {"exit": 0, "pipe_capacity": capacity}
        finally:
            child.close()
            peer.close()

    def hidden_password_utf8_crlf():
        peer = Peer()
        child = TTYClient(binary, peer.port, ["--auto-login", "--user", "admin", "--ask-pass"])
        try:
            child.wait_output(b"Password: ")
            child.wait_raw()
            child.send("密钥错".encode() + b"\x7f" + "码".encode() + b"\r\n")
            peer.send(b"Login:")
            peer.wait_bytes(b"admin\r\n")
            peer.send(b"Password:")
            password = "密钥码".encode()
            peer.wait_bytes(password + b"\r\n")
            peer.send(b"\r\nrouter#\r\nSESSION READY\r\n")
            child.wait_output(b"SESSION READY")
            child.wait_raw()
            child.send(b"probe\r")
            peer.wait_bytes(b"probe\r\n")
            child.quit()
            expected = b"admin\r\n" + password + b"\r\nprobe\r\n"
            assert bytes(peer.received) == expected, f"password edit or CRLF changed wire input: {bytes(peer.received)!r}"
            assert "密".encode() not in child.read(), "hidden password was echoed"
            assert "错".encode() not in child.read(), "deleted password character was echoed"
            return {"exit": 0, "hidden": True, "utf8_backspace": True, "extra_enter": False}
        finally:
            child.close()
            peer.close()

    for name, fn in [
        ("connect_failure_restores_terminal", failed_dial),
        ("connect_failure_preserves_nonblocking_terminal", lambda: failed_dial(True)),
        ("remote_eof_restores_terminal", remote_eof),
        ("escape_quit_is_local", escape_quit),
        ("quit_preserves_nonblocking_terminal", lambda: escape_quit(True)),
        ("sigterm_restores_terminal", terminated),
        ("naws_initial_and_resize", naws_resize),
        ("resize_during_login_is_refreshed", login_resize_refresh),
        ("remote_local_echo_transitions", echo_transitions),
        ("split_crlf_crnul_enter_mapping", split_newlines),
        ("pipe_eof_drains_final_output", pipe_half_close),
        ("default_pipe_eof_preserves_negotiation", pipe_eof_keeps_negotiation),
        ("sigterm_during_password_restores_terminal", password_signal),
        ("trace_private_and_no_overwrite", trace_permissions),
        ("stdout_write_error_is_failure", stdout_error),
        ("broken_stdout_pipe_restores_terminal", broken_stdout_pipe),
        ("local_ui_keeps_stdout_clean", redirected_stdout),
        ("sigterm_with_blocked_stdout_restores_terminal", blocked_stdout_signal),
        ("quit_with_blocked_stdout_restores_terminal", blocked_stdout_quit),
        ("hidden_password_utf8_edit_and_crlf", hidden_password_utf8_crlf),
    ]:
        run_case(name, fn, results)

    if args.out:
        with open(args.out, "w") as stream:
            json.dump(results, stream, indent=2)
    raise SystemExit(0 if all(item["ok"] for item in results) else 1)


if __name__ == "__main__":
    main()
