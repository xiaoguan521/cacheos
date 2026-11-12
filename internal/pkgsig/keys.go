package pkgsig

// builtinKeys 是正式的升级包签名公钥（离线签名机上 license-signer keygen -kid etos-pkg-<年份> 生成）。
// 只放公钥。etos-pkg-2027 为升级包签名主密钥，-b 为备用密钥；对应私钥只保存在离线签名机上。
// 两把公钥都内置，主密钥轮换或封存备用密钥时，已部署节点无需先升级即可验签。
var builtinKeys = map[string]string{
	"etos-pkg-2027":   "cHvZbmzVruA+nBpNeKLv8BZ4sHjdUS9W6n0XrLmIsn0=",
	"etos-pkg-2027-b": "JOM6HsRnAqo0ldQVLEkeIx5ISmTic4e6ZUIJko5VgUU=",
}
