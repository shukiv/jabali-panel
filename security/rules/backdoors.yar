rule backdoor_reverse_shell_bash {
    meta:
        author = "jabali-security"
        description = "Bash reverse shell command pattern"
        severity = "critical"
    strings:
        $bash_rev1 = /bash\s+-i\s+>&\s*\/dev\/tcp\// nocase
        $bash_rev2 = /\/bin\/bash\s+-i\s+>&\s*\/dev\/tcp\// nocase
        $bash_rev3 = /exec\s+\d+<>\/dev\/tcp\// nocase
        $bash_rev4 = /0<&\d+-;\s*exec\s+\d+<>\/dev\/tcp\// nocase
    condition:
        any of them
}

rule backdoor_reverse_shell_python {
    meta:
        author = "jabali-security"
        description = "Python reverse shell pattern"
        severity = "critical"
    strings:
        $py_rev1 = "socket.socket(socket.AF_INET" nocase
        $py_rev2 = "subprocess.call" nocase
        $py_rev3 = /os\.dup2\s*\(\s*s\.fileno\s*\(\s*\)/ nocase
        $py_rev4 = /pty\.spawn\s*\(\s*['"]\/bin\/(ba)?sh['"]/ nocase
        $py_import = "import socket" nocase
    condition:
        $py_import and ($py_rev1 or $py_rev3) and ($py_rev2 or $py_rev4)
}

rule backdoor_reverse_shell_perl {
    meta:
        author = "jabali-security"
        description = "Perl reverse shell pattern"
        severity = "critical"
    strings:
        $perl_socket = "use Socket;" nocase
        $perl_rev1 = /socket\s*\(\s*S\s*,\s*PF_INET/ nocase
        $perl_rev2 = /connect\s*\(\s*S\s*,\s*sockaddr_in/ nocase
        $perl_rev3 = /open\s*\(\s*STDIN\s*,\s*['"]\s*>&\s*S['"]/ nocase
        $perl_exec = /exec\s*\(\s*['"]\/bin\/(ba)?sh/ nocase
    condition:
        $perl_socket and 2 of ($perl_rev*, $perl_exec)
}

rule backdoor_reverse_shell_php {
    meta:
        author = "jabali-security"
        description = "PHP reverse shell pattern"
        severity = "critical"
    strings:
        $php_fsock = /fsockopen\s*\(\s*\$/ nocase
        $php_rev1 = /fsockopen\s*\(\s*['"][\d\.]+['"]/ nocase
        $php_rev2 = /\$sock\s*=\s*fsockopen/ nocase
        $php_shell = /shell_exec\s*\(\s*['"]\/bin\/(ba)?sh/ nocase
        $php_proc = /proc_open\s*\(\s*['"]\/bin\/(ba)?sh/ nocase
        $php_popen_sh = /popen\s*\(\s*['"]\/bin\/(ba)?sh\s+-i/ nocase
    condition:
        ($php_fsock or $php_rev1 or $php_rev2) and ($php_shell or $php_proc or $php_popen_sh)
}

rule backdoor_bind_shell {
    meta:
        author = "jabali-security"
        description = "Bind shell listener pattern (nc, socat, ncat)"
        severity = "critical"
    strings:
        $nc_listen1 = /\bnc\s+-[a-z]*l[a-z]*p?\s+\d+\s+-e\s+\/bin\/(ba)?sh/ nocase
        $nc_listen2 = /\bncat\s+-[a-z]*l[a-z]*\s+-e\s+\/bin\/(ba)?sh/ nocase
        $nc_listen3 = /\bnetcat\s+-[a-z]*l[a-z]*p?\s+\d+\s+-e/ nocase
        $socat_bind = /socat\s+TCP-LISTEN:\d+.*EXEC:\/bin\/(ba)?sh/ nocase
    condition:
        any of them
}

rule backdoor_php_cookie_eval {
    meta:
        author = "jabali-security"
        description = "PHP backdoor using cookie-based command execution"
        severity = "high"
    strings:
        $cookie_eval = /eval\s*\(\s*\$_COOKIE\s*\[/ nocase
        $cookie_assert = /assert\s*\(\s*\$_COOKIE\s*\[/ nocase
        $cookie_b64 = /eval\s*\(\s*base64_decode\s*\(\s*\$_COOKIE\s*\[/ nocase
        $cookie_system = /system\s*\(\s*\$_COOKIE\s*\[/ nocase
        $cookie_create = /create_function\s*\(\s*['"]['"]\s*,\s*\$_COOKIE/ nocase
    condition:
        any of them
}

rule backdoor_php_hidden_endpoint {
    meta:
        author = "jabali-security"
        description = "PHP backdoor hidden in legitimate-looking file with secret parameter"
        severity = "high"
    strings:
        $secret_param = /if\s*\(\s*isset\s*\(\s*\$_(GET|POST|REQUEST)\s*\[\s*['"][a-f0-9]{32}['"]\s*\]\s*\)\s*\)/ nocase
        $header_check = /if\s*\(\s*\$_SERVER\s*\[\s*['"]HTTP_/ nocase
        $hidden_eval = /eval\s*\(/ nocase
        $hidden_system = /system\s*\(/ nocase
        $hidden_exec = /\bexec\s*\(/ nocase
    condition:
        ($secret_param or $header_check) and ($hidden_eval or $hidden_system or $hidden_exec)
}

rule backdoor_cron_manipulation {
    meta:
        author = "jabali-security"
        description = "Script that manipulates cron jobs for persistence"
        severity = "high"
    strings:
        $crontab_write = /crontab\s+-/ nocase
        $cron_dir = "/etc/cron" nocase
        $cron_spool = "/var/spool/cron" nocase
        $cron_wget = /wget\s+.+\|\s*(ba)?sh/ nocase
        $cron_curl = /curl\s+.+\|\s*(ba)?sh/ nocase
        $cron_persist = /echo\s+.*>>\s*\/var\/spool\/cron/ nocase
        $cron_etc = /echo\s+.*>>\s*\/etc\/cron/ nocase
    condition:
        ($crontab_write or $cron_dir or $cron_spool) and ($cron_wget or $cron_curl or $cron_persist or $cron_etc)
}

rule backdoor_ssh_key_injection {
    meta:
        author = "jabali-security"
        description = "Unauthorized SSH authorized_keys manipulation"
        severity = "critical"
    strings:
        $auth_keys = ".ssh/authorized_keys" nocase
        $append_key = /echo\s+.*ssh-(rsa|ed25519|ecdsa).*>>\s*.*authorized_keys/ nocase
        $mkdir_ssh = /mkdir\s+.*\.ssh/ nocase
        $wget_key = /wget\s+.*>>\s*.*authorized_keys/ nocase
        $curl_key = /curl\s+.*>>\s*.*authorized_keys/ nocase
    condition:
        $auth_keys and 1 of ($append_key, $mkdir_ssh, $wget_key, $curl_key)
}

rule backdoor_hidden_user_creation {
    meta:
        author = "jabali-security"
        description = "Script creating hidden user accounts for persistence"
        severity = "critical"
    strings:
        $useradd = /useradd\s+/ nocase
        $passwd_file = "/etc/passwd" nocase
        $shadow_file = "/etc/shadow" nocase
        $append_passwd = /echo\s+.*:0:0:.*>>\s*\/etc\/passwd/ nocase
        $uid_zero = /:0:0:/ nocase
        $adduser = /adduser\s+/ nocase
    condition:
        $append_passwd or ($uid_zero and ($passwd_file or $shadow_file) and ($useradd or $adduser))
}

rule backdoor_php_error_handler {
    meta:
        author = "jabali-security"
        description = "PHP backdoor hiding code execution in error handler or shutdown function"
        severity = "high"
    strings:
        $err_handler = /set_error_handler\s*\(\s*['"]/ nocase
        $shutdown = /register_shutdown_function\s*\(/ nocase
        $eval_call = "eval(" nocase
        $b64_decode = "base64_decode(" nocase
        $create_func = "create_function(" nocase
    condition:
        ($err_handler or $shutdown) and ($eval_call or $create_func) and $b64_decode
}
