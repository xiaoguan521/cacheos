package cacheos;

import io.lettuce.core.ClientOptions;
import io.lettuce.core.RedisClient;
import io.lettuce.core.RedisURI;
import io.lettuce.core.ScriptOutputType;
import io.lettuce.core.SslOptions;
import io.lettuce.core.api.StatefulRedisConnection;
import io.lettuce.core.api.sync.RedisCommands;
import io.lettuce.core.cluster.RedisClusterClient;
import io.lettuce.core.cluster.api.StatefulRedisClusterConnection;
import io.lettuce.core.cluster.api.sync.RedisAdvancedClusterCommands;
import io.lettuce.core.protocol.ProtocolVersion;
import java.io.File;
import java.util.Arrays;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.stream.Collectors;
import org.redisson.Redisson;
import org.redisson.api.RBucket;
import org.redisson.api.RLock;
import org.redisson.api.RedissonClient;
import org.redisson.client.codec.StringCodec;
import org.redisson.config.Config;
import org.springframework.data.redis.connection.RedisClusterConfiguration;
import org.springframework.data.redis.connection.RedisConfiguration;
import org.springframework.data.redis.connection.RedisSentinelConfiguration;
import org.springframework.data.redis.connection.RedisStandaloneConfiguration;
import org.springframework.data.redis.connection.lettuce.LettuceConnectionFactory;
import org.springframework.data.redis.core.StringRedisTemplate;
import redis.clients.jedis.DefaultJedisClientConfig;
import redis.clients.jedis.HostAndPort;
import redis.clients.jedis.Jedis;
import redis.clients.jedis.JedisCluster;
import redis.clients.jedis.JedisPooled;
import redis.clients.jedis.JedisSentinelPool;
import redis.clients.jedis.RedisProtocol;

/**
 * Java 客户端对 sycache 的兼容性用例。拓扑由 client_matrix_test.py 通过环境变量传入：
 * STANDALONE_PORT、TLS_PORT、CLUSTER_PORTS、SENTINEL_PORTS（逗号分隔）、MASTER_NAME、TLS_CA、TLS_CERT、TLS_KEY。
 */
public final class ClientMatrix {
  private static final String H = "127.0.0.1";

  private ClientMatrix() {}

  private static int port(String name) {
    return Integer.parseInt(System.getenv(name));
  }

  private static List<Integer> ports(String name) {
    return Arrays.stream(System.getenv(name).split(",")).map(Integer::parseInt).collect(Collectors.toList());
  }

  private static void check(boolean ok, String msg) {
    if (!ok) {
      System.out.println("FAIL: " + msg);
      System.exit(1);
    }
    System.out.println("ok   " + msg);
  }

  public static void main(String[] args) {
    System.out.println("java clients: jedis 5.2.0, lettuce 6.5.5, redisson 3.45.1, spring-data-redis 3.4.5");
    try {
      jedis();
      lettuce();
      redisson();
      spring();
    } catch (Exception e) {
      System.out.println("FAIL: " + e);
      e.printStackTrace(System.out);
      System.exit(1);
    }
    System.exit(0); // Redisson / Netty 的后台线程不阻塞退出
  }

  static void jedis() {
    try (JedisPooled j = new JedisPooled(H, port("STANDALONE_PORT"))) {
      j.set("jv:k", "v");
      check("v".equals(j.get("jv:k")), "jedis standalone set/get");
      j.hset("jv:h", Map.of("a", "1", "b", "2"));
      check(j.hgetAll("jv:h").size() == 2, "jedis hash");
      Object r = j.eval("return redis.call('GET', KEYS[1])", List.of("jv:k"), List.of());
      check("v".equals(r), "jedis EVAL");
    }
    try (JedisPooled j = new JedisPooled(new HostAndPort(H, port("STANDALONE_PORT")),
        DefaultJedisClientConfig.builder().protocol(RedisProtocol.RESP3).build())) {
      j.set("jv:r3", "1");
      check("1".equals(j.get("jv:r3")), "jedis RESP3 set/get");
    }
    Set<HostAndPort> nodes = ports("CLUSTER_PORTS").stream().map(p -> new HostAndPort(H, p)).collect(Collectors.toSet());
    try (JedisCluster c = new JedisCluster(nodes)) {
      for (int i = 0; i < 200; i++) {
        c.set("jv:c:" + i, String.valueOf(i));
      }
      boolean ok = true;
      for (int i = 0; i < 200; i++) {
        ok &= String.valueOf(i).equals(c.get("jv:c:" + i));
      }
      check(ok, "jedis cluster 200 keys across slots");
    }
    Set<String> sentinels = ports("SENTINEL_PORTS").stream().map(p -> H + ":" + p).collect(Collectors.toSet());
    try (JedisSentinelPool pool = new JedisSentinelPool(System.getenv("MASTER_NAME"), sentinels);
        Jedis j = pool.getResource()) {
      j.set("jv:s", "1");
      check("1".equals(j.get("jv:s")), "jedis sentinel pool write/read");
    }
  }

  static void lettuce() {
    RedisClient client = RedisClient.create(RedisURI.create(H, port("STANDALONE_PORT")));
    for (ProtocolVersion pv : ProtocolVersion.values()) {
      client.setOptions(ClientOptions.builder().protocolVersion(pv).build());
      try (StatefulRedisConnection<String, String> conn = client.connect()) {
        RedisCommands<String, String> s = conn.sync();
        s.set("lt:k", "v");
        check("v".equals(s.get("lt:k")), "lettuce " + pv + " set/get");
        String r = s.eval("return redis.call('GET', KEYS[1])", ScriptOutputType.VALUE, "lt:k");
        check("v".equals(r), "lettuce " + pv + " EVAL");
      }
    }
    client.shutdown();

    List<RedisURI> seeds = ports("CLUSTER_PORTS").stream().map(p -> RedisURI.create(H, p)).collect(Collectors.toList());
    RedisClusterClient cc = RedisClusterClient.create(seeds);
    try (StatefulRedisClusterConnection<String, String> conn = cc.connect()) {
      RedisAdvancedClusterCommands<String, String> s = conn.sync();
      for (int i = 0; i < 200; i++) {
        s.set("lt:c:" + i, String.valueOf(i));
      }
      boolean ok = true;
      for (int i = 0; i < 200; i++) {
        ok &= String.valueOf(i).equals(s.get("lt:c:" + i));
      }
      check(ok, "lettuce cluster 200 keys across slots");
    }
    cc.shutdown();

    List<Integer> sp = ports("SENTINEL_PORTS");
    RedisURI.Builder sb = RedisURI.Builder.sentinel(H, sp.get(0), System.getenv("MASTER_NAME"));
    for (int i = 1; i < sp.size(); i++) {
      sb.withSentinel(H, sp.get(i));
    }
    RedisClient sc = RedisClient.create(sb.build());
    try (StatefulRedisConnection<String, String> conn = sc.connect()) {
      conn.sync().set("lt:s", "1");
      check("1".equals(conn.sync().get("lt:s")), "lettuce sentinel write/read");
    }
    sc.shutdown();

    SslOptions ssl = SslOptions.builder().jdkSslProvider()
        .trustManager(new File(System.getenv("TLS_CA")))
        .keyManager(new File(System.getenv("TLS_CERT")), new File(System.getenv("TLS_KEY")), null)
        .build();
    RedisURI tu = RedisURI.builder().withHost(H).withPort(port("TLS_PORT")).withSsl(true).withVerifyPeer(true).build();
    RedisClient tc = RedisClient.create(tu);
    tc.setOptions(ClientOptions.builder().sslOptions(ssl).build());
    try (StatefulRedisConnection<String, String> conn = tc.connect()) {
      conn.sync().set("lt:tls", "1");
      check("1".equals(conn.sync().get("lt:tls")), "lettuce TLS mutual auth set/get");
    }
    tc.shutdown();
  }

  static void redisson() {
    Config single = new Config();
    single.useSingleServer().setAddress("redis://" + H + ":" + port("STANDALONE_PORT"));
    RedissonClient r = Redisson.create(single);
    try {
      RBucket<String> b = r.getBucket("rs:k", StringCodec.INSTANCE);
      b.set("v");
      check("v".equals(b.get()), "redisson bucket set/get");
      RLock lock = r.getLock("rs:lock");
      lock.lock();
      check(lock.isHeldByCurrentThread(), "redisson distributed lock (Lua)");
      lock.unlock();
    } finally {
      r.shutdown();
    }

    Config cluster = new Config();
    cluster.useClusterServers().addNodeAddress(
        ports("CLUSTER_PORTS").stream().map(p -> "redis://" + H + ":" + p).toArray(String[]::new));
    RedissonClient rc = Redisson.create(cluster);
    try {
      for (int i = 0; i < 100; i++) {
        rc.getBucket("rs:c:" + i, StringCodec.INSTANCE).set(String.valueOf(i));
      }
      check("42".equals(rc.getBucket("rs:c:42", StringCodec.INSTANCE).get()), "redisson cluster buckets");
    } finally {
      rc.shutdown();
    }

    Config sentinel = new Config();
    sentinel.useSentinelServers().setMasterName(System.getenv("MASTER_NAME")).addSentinelAddress(
        ports("SENTINEL_PORTS").stream().map(p -> "redis://" + H + ":" + p).toArray(String[]::new));
    RedissonClient rs = Redisson.create(sentinel);
    try {
      RBucket<String> b = rs.getBucket("rs:s", StringCodec.INSTANCE);
      b.set("1");
      check("1".equals(b.get()), "redisson sentinel write/read");
    } finally {
      rs.shutdown();
    }
  }

  static void spring() {
    springCase("standalone", new RedisStandaloneConfiguration(H, port("STANDALONE_PORT")));
    springCase("cluster", new RedisClusterConfiguration(
        ports("CLUSTER_PORTS").stream().map(p -> H + ":" + p).collect(Collectors.toList())));
    RedisSentinelConfiguration sc = new RedisSentinelConfiguration().master(System.getenv("MASTER_NAME"));
    for (int p : ports("SENTINEL_PORTS")) {
      sc.sentinel(H, p);
    }
    springCase("sentinel", sc);
  }

  private static void springCase(String name, RedisConfiguration cfg) {
    LettuceConnectionFactory f;
    if (cfg instanceof RedisStandaloneConfiguration s) {
      f = new LettuceConnectionFactory(s);
    } else if (cfg instanceof RedisClusterConfiguration c) {
      f = new LettuceConnectionFactory(c);
    } else {
      f = new LettuceConnectionFactory((RedisSentinelConfiguration) cfg);
    }
    f.afterPropertiesSet();
    try {
      StringRedisTemplate t = new StringRedisTemplate(f);
      t.opsForValue().set("sp:" + name, "v");
      check("v".equals(t.opsForValue().get("sp:" + name)), "spring-data-redis " + name + " set/get");
    } finally {
      f.destroy();
    }
  }
}
