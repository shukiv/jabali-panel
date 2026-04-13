rule miner_xmrig_binary {
    meta:
        author = "jabali-security"
        description = "XMRig cryptocurrency miner binary or configuration"
        severity = "critical"
    strings:
        $xmrig1 = "xmrig" nocase
        $xmrig2 = "XMRig" nocase
        $xmrig_url = "donate.v2.xmrig.com" nocase
        $xmrig_agent = "\"user-agent\"" nocase
        $xmrig_algo = "\"algo\"" nocase
        $xmrig_pool = "\"pool\"" nocase
        $xmrig_coin = "\"coin\"" nocase
        $randomx = "randomx" nocase
        $cryptonight = "cryptonight" nocase
    condition:
        ($xmrig1 or $xmrig2) and 2 of ($xmrig_url, $xmrig_agent, $xmrig_algo, $xmrig_pool, $xmrig_coin, $randomx, $cryptonight)
}

rule miner_known_binary {
    meta:
        author = "jabali-security"
        description = "Known cryptocurrency mining software (cgminer, cpuminer, etc.)"
        severity = "critical"
    strings:
        $cgminer = "cgminer" nocase
        $bfgminer = "bfgminer" nocase
        $cpuminer = "cpuminer" nocase
        $minerd = "minerd" nocase
        $ethminer = "ethminer" nocase
        $t_rex = "t-rex miner" nocase
        $nbminer = "nbminer" nocase
        $phoenixminer = "phoenixminer" nocase
        $stratum = "stratum+tcp://" nocase
        $stratumssl = "stratum+ssl://" nocase
    condition:
        any of ($cgminer, $bfgminer, $cpuminer, $minerd, $ethminer, $t_rex, $nbminer, $phoenixminer) and ($stratum or $stratumssl)
}

rule miner_pool_connection {
    meta:
        author = "jabali-security"
        description = "Connection strings for known mining pools"
        severity = "high"
    strings:
        $pool1 = "pool.minexmr.com" nocase
        $pool2 = "pool.supportxmr.com" nocase
        $pool3 = "xmr.nanopool.org" nocase
        $pool4 = "monerohash.com" nocase
        $pool5 = "moneroocean.stream" nocase
        $pool6 = "hashvault.pro" nocase
        $pool7 = "herominers.com" nocase
        $pool8 = "2miners.com" nocase
        $pool9 = "f2pool.com" nocase
        $pool10 = "unmineable.com" nocase
        $pool11 = "nicehash.com" nocase
        $pool12 = "minergate.com" nocase
    condition:
        any of them
}

rule miner_stratum_protocol {
    meta:
        author = "jabali-security"
        description = "Mining stratum protocol communication patterns"
        severity = "high"
    strings:
        $stratum_uri = /stratum\+tcp:\/\/[a-zA-Z0-9\.\-]+:\d+/ nocase
        $stratum_ssl = /stratum\+ssl:\/\/[a-zA-Z0-9\.\-]+:\d+/ nocase
        $stratum_login = "mining.authorize" nocase
        $stratum_submit = "mining.submit" nocase
        $stratum_subscribe = "mining.subscribe" nocase
        $stratum_notify = "mining.notify" nocase
    condition:
        ($stratum_uri or $stratum_ssl) or 2 of ($stratum_login, $stratum_submit, $stratum_subscribe, $stratum_notify)
}

rule miner_wallet_address {
    meta:
        author = "jabali-security"
        description = "Cryptocurrency wallet address patterns in config context"
        severity = "medium"
    strings:
        $monero_addr = /4[0-9AB][1-9A-HJ-NP-Za-km-z]{93}/ nocase
        $bitcoin_addr = /bc1[a-zA-HJ-NP-Z0-9]{39,59}/ nocase
        $pool_keyword = "pool" nocase
        $worker_keyword = "worker" nocase
        $wallet_keyword = "wallet" nocase
        $mining_keyword = "mining" nocase
    condition:
        ($monero_addr or $bitcoin_addr) and 2 of ($pool_keyword, $worker_keyword, $wallet_keyword, $mining_keyword)
}

rule miner_javascript_browser {
    meta:
        author = "jabali-security"
        description = "JavaScript browser-based cryptocurrency miner"
        severity = "high"
    strings:
        $coinhive = "CoinHive" nocase
        $coinhive_min = "coinhive.min.js" nocase
        $cryptoloot = "CryptoLoot" nocase
        $cryptonight_wasm = "cryptonight.wasm" nocase
        $webmine = "webmine.pro" nocase
        $jsecoin = "jsecoin" nocase
        $minero = "minero.cc" nocase
        $deepminer = "deepMiner" nocase
        $webmr = "webmr.js" nocase
        $wasm_miner = /new\s+Worker\s*\(\s*['"].*wasm/ nocase
    condition:
        any of them
}

rule miner_config_file {
    meta:
        author = "jabali-security"
        description = "Cryptocurrency mining configuration file"
        severity = "high"
    strings:
        $cfg_pool = "\"url\"" nocase
        $cfg_user = "\"user\"" nocase
        $cfg_pass = "\"pass\"" nocase
        $cfg_algo = "\"algo\"" nocase
        $cfg_threads = "\"threads\"" nocase
        $cfg_cpu = "\"cpu\"" nocase
        $stratum = "stratum+" nocase
        $algo_cn = "cryptonight" nocase
        $algo_rx = "randomx" nocase
        $algo_kawpow = "kawpow" nocase
    condition:
        $stratum and $cfg_pool and $cfg_user and 2 of ($cfg_pass, $cfg_algo, $cfg_threads, $cfg_cpu, $algo_cn, $algo_rx, $algo_kawpow)
}
