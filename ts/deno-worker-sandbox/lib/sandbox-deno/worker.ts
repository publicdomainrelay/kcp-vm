interface ExecuteMessage {
  type: "execute";
  id: number;
  code: string;
  timeoutMs: number;
}

interface ShutdownMessage {
  type: "shutdown";
}

function reply(msg: Record<string, unknown>) {
  self.postMessage(msg);
}

self.onmessage = async (ev: MessageEvent) => {
  const msg = ev.data as Record<string, unknown>;

  switch (msg.type) {
    case "execute": {
      const { id, code, timeoutMs } = msg as unknown as ExecuteMessage;
      const captured = { stdout: "", stderr: "" };

      const origLog = console.log;
      const origError = console.error;
      console.log = (...args: unknown[]) => {
        captured.stdout += args.map(String).join(" ") + "\n";
      };
      console.error = (...args: unknown[]) => {
        captured.stderr += args.map(String).join(" ") + "\n";
      };

      try {
        let timedOut = false;
        let result: unknown;

        if (timeoutMs > 0) {
          const timeoutPromise = new Promise<never>((_, reject) =>
            setTimeout(() => {
              timedOut = true;
              reject(new Error("timeout"));
            }, timeoutMs)
          );
          const fn = new Function(code) as () => unknown;
          result = await Promise.race([Promise.resolve(fn()), timeoutPromise]);
        } else {
          const fn = new Function(code) as () => unknown;
          result = await Promise.resolve(fn());
        }

        reply({
          type: "result",
          id,
          stdout: captured.stdout,
          stderr: "",
          exitCode: 0,
          timedOut: false,
          result,
        });
      } catch (err) {
        const errMsg = err instanceof Error ? err.message : String(err);
        reply({
          type: "result",
          id,
          stdout: captured.stdout,
          stderr: captured.stderr + errMsg,
          exitCode: 1,
          timedOut: errMsg === "timeout",
        });
      } finally {
        console.log = origLog;
        console.error = origError;
      }
      break;
    }

    case "shutdown": {
      reply({ type: "stopped" });
      self.close();
      break;
    }
  }
};
