import { createPersistentDenoWorker } from "@publicdomainrelay/sandbox-deno";
import { assertEquals } from "@std/assert";

function echoWorkerUrl(): string {
  const code = `
    self.onmessage = (ev) => {
      const m = ev.data;
      if (m.type === "ping") {
        self.postMessage({ type: "pong", id: m.id });
      } else if (m.type === "echo") {
        self.postMessage({ type: "echo_resp", payload: m.payload });
      } else if (m.type === "shutdown") {
        self.close();
      }
    };
  `;
  return `data:application/javascript;base64,${btoa(code)}`;
}

Deno.test("PersistentWorker sends and receives messages", async () => {
  const pw = createPersistentDenoWorker(echoWorkerUrl());
  try {
    const response = await new Promise<unknown>((resolve) => {
      pw.onMessage((msg) => resolve(msg));
      pw.postMessage({ type: "ping", id: 1 });
    });
    const m = response as Record<string, unknown>;
    assertEquals(m.type, "pong");
    assertEquals(m.id, 1);
  } finally {
    await pw.shutdown();
  }
});

Deno.test("PersistentWorker survives multiple messages", async () => {
  const pw = createPersistentDenoWorker(echoWorkerUrl());
  try {
    for (let i = 0; i < 3; i++) {
      const response = await new Promise<unknown>((resolve) => {
        pw.onMessage((msg) => resolve(msg));
        pw.postMessage({ type: "ping", id: i });
      });
      const m = response as Record<string, unknown>;
      assertEquals(m.type, "pong");
      assertEquals(m.id, i);
    }
  } finally {
    await pw.shutdown();
  }
});

Deno.test("PersistentWorker round-trips payload via echo", async () => {
  const pw = createPersistentDenoWorker(echoWorkerUrl());
  try {
    const response = await new Promise<unknown>((resolve) => {
      pw.onMessage((msg) => resolve(msg));
      pw.postMessage({ type: "echo", payload: { a: 1, b: [2, 3] } });
    });
    const m = response as Record<string, unknown>;
    assertEquals(m.type, "echo_resp");
    assertEquals(m.payload, { a: 1, b: [2, 3] });
  } finally {
    await pw.shutdown();
  }
});

Deno.test("PersistentWorker shutdown terminates worker", async () => {
  const pw = createPersistentDenoWorker(echoWorkerUrl());
  await pw.shutdown();

  let msgReceived = false;
  pw.onMessage(() => {
    msgReceived = true;
  });
  pw.postMessage({ type: "ping", id: 1 });

  await new Promise((resolve) => setTimeout(resolve, 100));
  assertEquals(msgReceived, false);
});
