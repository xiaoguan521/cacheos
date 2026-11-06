using System.Net;
using System.Security.Authentication;
using System.Security.Cryptography.X509Certificates;
using System.Text.Json;
using StackExchange.Redis;

using var topologyDocument = JsonDocument.Parse(Environment.GetEnvironmentVariable("TOPOLOGY")
    ?? throw new InvalidOperationException("TOPOLOGY is required"));
var topology = topologyDocument.RootElement;
const string Host = "127.0.0.1";

int Port(string name) => topology.GetProperty(name).GetInt32();
int[] Ports(string name) => topology.GetProperty(name).EnumerateArray().Select(x => x.GetInt32()).ToArray();

void Report(string name) => Console.WriteLine($"ok dotnet {name}");
void Check(bool condition, string message)
{
    if (!condition) throw new InvalidOperationException(message);
}

async Task StandaloneAsync()
{
    await using var connection = await ConnectionMultiplexer.ConnectAsync($"{Host}:{Port("standalone")}");
    var db = connection.GetDatabase();
    await db.StringSetAsync("stackexchange:standalone", "value");
    Check((await db.StringGetAsync("stackexchange:standalone")) == "value", "standalone GET mismatch");
    await db.HashSetAsync("stackexchange:hash", new HashEntry[] { new("a", "1"), new("b", "2") });
    Check((await db.HashLengthAsync("stackexchange:hash")) == 2, "standalone hash mismatch");
    Report("StackExchange.Redis standalone");
}

async Task ClusterAsync()
{
    var options = new ConfigurationOptions
    {
        AbortOnConnectFail = false,
        AllowAdmin = true,
        TieBreaker = "",
        ConnectTimeout = 30000,
        SyncTimeout = 30000,
        ConnectRetry = 10,
        ReconnectRetryPolicy = new LinearRetry(1000)
    };
    var clusterPorts = Ports("cluster");
    options.EndPoints.Add(Host, clusterPorts[0]);
    await using var connection = await ConnectionMultiplexer.ConnectAsync(options);
    var db = connection.GetDatabase();
    for (var i = 0; i < 100; i++) await db.StringSetAsync($"stackexchange:cluster:{i}", i.ToString());
    for (var i = 0; i < 100; i++)
        Check((await db.StringGetAsync($"stackexchange:cluster:{i}")) == i.ToString(), "cluster GET mismatch");
    Report("StackExchange.Redis cluster");
}

async Task SentinelAsync()
{
    var serviceName = topology.GetProperty("master_name").GetString()!;
    var sentinelOptions = new ConfigurationOptions
    {
        AbortOnConnectFail = false,
        CommandMap = CommandMap.Sentinel,
        TieBreaker = ""
    };
    var sentinels = Ports("sentinels");
    foreach (var p in sentinels) sentinelOptions.EndPoints.Add(Host, p);
    await using var sentinel = await ConnectionMultiplexer.ConnectAsync(sentinelOptions);
    var server = sentinel.GetServer(Host, sentinels[0]);
    var master = server.SentinelGetMasterAddressByName(serviceName);
    Check(master is IPEndPoint, "sentinel did not return a master endpoint");

    var dataOptions = new ConfigurationOptions { AbortOnConnectFail = false };
    dataOptions.EndPoints.Add(master!);
    await using var connection = await ConnectionMultiplexer.ConnectAsync(dataOptions);
    var db = connection.GetDatabase();
    await db.StringSetAsync("stackexchange:sentinel", "value");
    Check((await db.StringGetAsync("stackexchange:sentinel")) == "value", "sentinel GET mismatch");
    Report("StackExchange.Redis sentinel");
}

X509Certificate2 LoadClientCertificate(JsonElement certs)
    => X509Certificate2.CreateFromPemFile(certs.GetProperty("client.crt").GetString()!,
        certs.GetProperty("client.key").GetString()!);

ConfigurationOptions TlsOptions(JsonElement certs)
{
    var options = new ConfigurationOptions
    {
        AbortOnConnectFail = false,
        Ssl = true,
        SslHost = Host,
        SslProtocols = SslProtocols.Tls12 | SslProtocols.Tls13,
        ConnectTimeout = 10000
    };
    options.EndPoints.Add(Host, Port("tls"));
    var clientCertificate = LoadClientCertificate(certs);
    var caCertificate = new X509Certificate2(certs.GetProperty("ca.crt").GetString()!);
    options.CertificateSelection += (_, _, _, _, _) => clientCertificate;
    options.CertificateValidation += (_, certificate, _, _) =>
    {
        if (certificate is null) return false;
        using var chain = new X509Chain();
        chain.ChainPolicy.TrustMode = X509ChainTrustMode.CustomRootTrust;
        chain.ChainPolicy.CustomTrustStore.Add(caCertificate);
        chain.ChainPolicy.RevocationMode = X509RevocationMode.NoCheck;
        return chain.Build(new X509Certificate2(certificate));
    };
    return options;
}

async Task TlsAsync()
{
    var certs = topology.GetProperty("certs");
    await using var connection = await ConnectionMultiplexer.ConnectAsync(TlsOptions(certs));
    var db = connection.GetDatabase();
    await db.StringSetAsync("stackexchange:tls", "value");
    Check((await db.StringGetAsync("stackexchange:tls")) == "value", "TLS GET mismatch");
    Report("StackExchange.Redis mutual TLS");
}

try
{
    await StandaloneAsync();
    await ClusterAsync();
    await SentinelAsync();
    await TlsAsync();
}
catch (Exception error)
{
    Console.Error.WriteLine(error);
    Environment.ExitCode = 1;
}
