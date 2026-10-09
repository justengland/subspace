import index from "./index.html";

const port = Number(process.env.SUBSPACE_FRONT_PORT ?? 4200);

Bun.serve({
  port,
  hostname: "127.0.0.1",
  development: true,
  routes: {
    "/": index,
    "/workflows/:repo": index,
    "/workflows/:repo/:workflowId": index,
    "/runs/:repo/:runId": index,
  },
});

console.log(`http://127.0.0.1:${port}/`);
