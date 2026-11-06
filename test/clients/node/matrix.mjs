import fs from "node:fs";
import { createClient, createCluster } from "redis";
import Redis from "ioredis";

const topology = JSON.parse(process.env.TOPOLOGY);
const host = "127.0.0.1";

function port(name) {
  return Number(topology[name]);
}

function report(message) {
  console.log(`ok node ${message}`);
}

async function closeRedis(client) {
  if (!client) return;
  try {
    await client.quit();
  } catch (_) {
    client.disconnect();
  }
}

async function closeIoredis(client) {
  if (!client) return;
  try {
    await client.quit();
  } catch (_) {
    client.disconnect();
  }
}

async function nodeRedis() {
  let client;
  try {
    client = createClient({ url: `redis://${host}:${port("standalone")}` });
    client.on("error", (error) => console.error(`node-redis: ${error.message}`));
    await client.connect();
    await client.set("node-redis:standalone", "value");
    if ((await client.get("node-redis:standalone")) !== "value") throw new Error("standalone GET mismatch");
    await client.hSet("node-redis:hash", { a: "1", b: "2" });
    const hash = await client.hGetAll("node-redis:hash");
    if (hash.a !== "1" || hash.b !== "2") throw new Error("standalone hash mismatch");
    const value = await client.eval("return redis.call('GET', KEYS[1])", {
      keys: ["node-redis:standalone"],
      arguments: [],
    });
    if (value !== "value") throw new Error("standalone EVAL mismatch");
    report("node-redis standalone RESP2 and Lua");
  } finally {
    await closeRedis(client);
  }

  let resp3;
  try {
    resp3 = createClient({
      RESP: 3,
      url: `redis://${host}:${port("standalone")}`,
    });
    resp3.on("error", (error) => console.error(`node-redis RESP3: ${error.message}`));
    await resp3.connect();
    await resp3.set("node-redis:resp3", "value");
    if ((await resp3.get("node-redis:resp3")) !== "value") throw new Error("RESP3 GET mismatch");
    report("node-redis RESP3");
  } finally {
    await closeRedis(resp3);
  }

  let cluster;
  try {
    cluster = createCluster({
      rootNodes: topology.cluster.map((p) => ({ url: `redis://${host}:${p}` })),
    });
    cluster.on("error", (error) => console.error(`node-redis cluster: ${error.message}`));
    await cluster.connect();
    for (let i = 0; i < 100; i++) await cluster.set(`node-redis:cluster:${i}`, String(i));
    for (let i = 0; i < 100; i++) {
      if ((await cluster.get(`node-redis:cluster:${i}`)) !== String(i)) throw new Error("cluster GET mismatch");
    }
    report("node-redis cluster");
  } finally {
    await closeRedis(cluster);
  }

  let tls;
  try {
    const certs = topology.certs;
    tls = createClient({
      socket: {
        host,
        port: port("tls"),
        tls: true,
        ca: fs.readFileSync(certs["ca.crt"]),
        cert: fs.readFileSync(certs["client.crt"]),
        key: fs.readFileSync(certs["client.key"]),
        rejectUnauthorized: true,
      },
    });
    tls.on("error", (error) => console.error(`node-redis TLS: ${error.message}`));
    await tls.connect();
    await tls.set("node-redis:tls", "value");
    if ((await tls.get("node-redis:tls")) !== "value") throw new Error("TLS GET mismatch");
    report("node-redis mutual TLS");
  } finally {
    await closeRedis(tls);
  }
}

async function ioredis() {
  let client;
  try {
    client = new Redis({ host, port: port("standalone"), lazyConnect: true });
    await client.connect();
    await client.set("ioredis:standalone", "value");
    if ((await client.get("ioredis:standalone")) !== "value") throw new Error("standalone GET mismatch");
    const value = await client.eval("return redis.call('GET', KEYS[1])", 1, "ioredis:standalone");
    if (value !== "value") throw new Error("standalone EVAL mismatch");
    report("ioredis standalone and Lua");
  } finally {
    await closeIoredis(client);
  }

  let cluster;
  try {
    cluster = new Redis.Cluster(
      topology.cluster.map((p) => ({ host, port: p })),
      { lazyConnect: true },
    );
    await cluster.connect();
    for (let i = 0; i < 100; i++) await cluster.set(`ioredis:cluster:${i}`, String(i));
    for (let i = 0; i < 100; i++) {
      if ((await cluster.get(`ioredis:cluster:${i}`)) !== String(i)) throw new Error("cluster GET mismatch");
    }
    report("ioredis cluster");
  } finally {
    await closeIoredis(cluster);
  }

  let sentinel;
  try {
    sentinel = new Redis({
      sentinels: topology.sentinels.map((p) => ({ host, port: p })),
      name: topology.master_name,
      lazyConnect: true,
    });
    await sentinel.connect();
    await sentinel.set("ioredis:sentinel", "value");
    if ((await sentinel.get("ioredis:sentinel")) !== "value") throw new Error("sentinel GET mismatch");
    report("ioredis sentinel");
  } finally {
    await closeIoredis(sentinel);
  }

  let tls;
  try {
    const certs = topology.certs;
    tls = new Redis({
      host,
      port: port("tls"),
      lazyConnect: true,
      tls: {
        ca: fs.readFileSync(certs["ca.crt"]),
        cert: fs.readFileSync(certs["client.crt"]),
        key: fs.readFileSync(certs["client.key"]),
        rejectUnauthorized: true,
      },
    });
    await tls.connect();
    await tls.set("ioredis:tls", "value");
    if ((await tls.get("ioredis:tls")) !== "value") throw new Error("TLS GET mismatch");
    report("ioredis mutual TLS");
  } finally {
    await closeIoredis(tls);
  }
}

try {
  await nodeRedis();
  await ioredis();
} catch (error) {
  console.error(error?.stack || error);
  process.exitCode = 1;
}
