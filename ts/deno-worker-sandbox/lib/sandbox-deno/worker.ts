interface WorkerCtx {
  postMessage(msg: unknown): void;
  onmessage: ((ev: MessageEvent) => void) | null;
  close(): void;
}

const ctx = self as unknown as WorkerCtx;

interface ExecuteMessage {
  type: "execute";
  id: number;
  code: string;
}

function reply(msg: Record<string, unknown>) {
  ctx.postMessage(msg);
}

ctx.onmessage = async (ev: MessageEvent) => {
  const msg = ev.data as Record<string, unknown>;

  switch (msg.type) {
    case "execute": {
      const { id, code } = msg as unknown as ExecuteMessage;
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
        const wrapped = `return (async () => { ${code} })()`;
        const fn = new Function(wrapped) as () => Promise<unknown>;
        const result = await fn();

        reply({
          type: "result",
          id,
          stdout: captured.stdout,
          stderr: captured.stderr,
          exitCode: 0,
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
        });
      } finally {
        console.log = origLog;
        console.error = origError;
      }
      break;
    }

    case "shutdown": {
      reply({ type: "stopped" });
      ctx.close();
      break;
    }
  }
};
