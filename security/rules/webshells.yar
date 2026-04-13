rule webshell_php_eval_post {
    meta:
        author = "jabali-security"
        description = "PHP webshell using eval with POST/REQUEST/GET data"
        severity = "high"
    strings:
        $eval_post = /eval\s*\(\s*\$_(POST|REQUEST|GET)\s*\[/ nocase
        $eval_b64_post = /eval\s*\(\s*base64_decode\s*\(\s*\$_(POST|REQUEST|GET)/ nocase
        $eval_gzinflate = /eval\s*\(\s*gzinflate\s*\(\s*base64_decode\s*\(\s*\$_(POST|REQUEST|GET)/ nocase
    condition:
        any of them
}

rule webshell_php_assert_input {
    meta:
        author = "jabali-security"
        description = "PHP webshell using assert with user-controlled input"
        severity = "high"
    strings:
        $assert_post = /assert\s*\(\s*\$_(POST|REQUEST|GET)\s*\[/ nocase
        $assert_b64 = /assert\s*\(\s*base64_decode\s*\(\s*\$_(POST|REQUEST|GET)/ nocase
    condition:
        any of them
}

rule webshell_php_system_passthru {
    meta:
        author = "jabali-security"
        description = "PHP webshell executing system commands via user input"
        severity = "critical"
    strings:
        $system = /system\s*\(\s*\$_(POST|REQUEST|GET)\s*\[/ nocase
        $passthru = /passthru\s*\(\s*\$_(POST|REQUEST|GET)\s*\[/ nocase
        $exec = /\bexec\s*\(\s*\$_(POST|REQUEST|GET)\s*\[/ nocase
        $shell_exec = /shell_exec\s*\(\s*\$_(POST|REQUEST|GET)\s*\[/ nocase
        $popen = /popen\s*\(\s*\$_(POST|REQUEST|GET)\s*\[/ nocase
        $proc_open = /proc_open\s*\(\s*\$_(POST|REQUEST|GET)\s*\[/ nocase
    condition:
        any of them
}

rule webshell_php_oneliner {
    meta:
        author = "jabali-security"
        description = "Minimal PHP one-liner webshell pattern"
        severity = "high"
    strings:
        $oneliner1 = /\<\?php\s+@?eval\s*\(\s*\$_(POST|REQUEST|GET)\s*\[/ nocase
        $oneliner2 = /\<\?php\s+@?system\s*\(\s*\$_(POST|REQUEST|GET)\s*\[/ nocase
        $oneliner3 = /\<\?php\s+@?passthru\s*\(\s*\$_(POST|REQUEST|GET)\s*\[/ nocase
        $oneliner4 = /\<\?=\s*`\$_(GET|POST|REQUEST)/ nocase
    condition:
        any of them
}

rule webshell_php_obfuscated_eval {
    meta:
        author = "jabali-security"
        description = "Obfuscated PHP webshell using gzinflate/str_rot13/base64 chains"
        severity = "high"
    strings:
        $gz_b64 = "eval(gzinflate(base64_decode(" nocase
        $gz_str_rot = "eval(gzinflate(str_rot13(base64_decode(" nocase
        $rot13_eval = "eval(str_rot13(" nocase
        $gzuncompress = "eval(gzuncompress(base64_decode(" nocase
        $b64_multi = /eval\s*\(\s*base64_decode\s*\(\s*base64_decode/ nocase
    condition:
        any of them
}

rule webshell_php_preg_replace_eval {
    meta:
        author = "jabali-security"
        description = "PHP webshell using preg_replace with /e modifier for code execution"
        severity = "high"
    strings:
        $preg_e = /preg_replace\s*\(\s*['"\/].+\/e['"\/]\s*,\s*\$_(POST|REQUEST|GET)/ nocase
        $preg_e_b64 = /preg_replace\s*\(\s*['"\/].+\/e['"\/]\s*,\s*base64_decode/ nocase
    condition:
        any of them
}

rule webshell_php_create_function {
    meta:
        author = "jabali-security"
        description = "PHP webshell using create_function for dynamic code execution"
        severity = "high"
    strings:
        $create_func_post = /create_function\s*\(\s*['"]['"]\s*,\s*\$_(POST|REQUEST|GET)/ nocase
        $create_func_b64 = /create_function\s*\(\s*['"]['"]\s*,\s*base64_decode/ nocase
        $create_func_eval = /create_function\s*\(\s*['"].*['"]\s*,\s*['"].*eval/ nocase
    condition:
        any of them
}

rule webshell_php_variable_function {
    meta:
        author = "jabali-security"
        description = "PHP webshell using variable function calls for obfuscation"
        severity = "medium"
    strings:
        $var_func1 = /\$[a-zA-Z_]+\s*=\s*['"]base64_decode['"]/ nocase
        $var_func2 = /\$[a-zA-Z_]+\s*=\s*['"]system['"]/ nocase
        $var_func3 = /\$[a-zA-Z_]+\s*=\s*['"]passthru['"]/ nocase
        $var_func4 = /\$[a-zA-Z_]+\s*=\s*['"]shell_exec['"]/ nocase
        $var_call = /\$[a-zA-Z_]+\s*\(\s*\$_(POST|REQUEST|GET)/ nocase
    condition:
        any of ($var_func*) and $var_call
}

rule webshell_c99 {
    meta:
        author = "jabali-security"
        description = "c99 webshell family detection"
        severity = "critical"
    strings:
        $c99_title = "c99shell" nocase
        $c99_v1 = "c99_sess_put" nocase
        $c99_v2 = "c99sh_surl" nocase
        $c99_v3 = "c99_buff_prepare" nocase
        $c99_v4 = "c99fsearch" nocase
        $c99_header = "Encoder Tools" nocase
        $c99_selfrem = "Self remove" nocase
    condition:
        2 of them
}

rule webshell_r57 {
    meta:
        author = "jabali-security"
        description = "r57 webshell family detection"
        severity = "critical"
    strings:
        $r57_title = "r57shell" nocase
        $r57_v1 = "r57_pwd_color" nocase
        $r57_v2 = "r57_login" nocase
        $r57_v3 = "r57shell.php" nocase
        $r57_feat = "Safe mode" nocase
        $r57_backconn = "Back-Connect" nocase
    condition:
        2 of ($r57_*)
}

rule webshell_wso {
    meta:
        author = "jabali-security"
        description = "WSO webshell family detection"
        severity = "critical"
    strings:
        $wso_title = "WSO " nocase
        $wso_v1 = "Web Shell by oRb" nocase
        $wso_v2 = "wso_version" nocase
        $wso_v3 = "FilesMan" nocase
        $wso_auth = "md5(md5(" nocase
        $wso_feat1 = "Ede43f" nocase
    condition:
        2 of them
}

rule webshell_b374k {
    meta:
        author = "jabali-security"
        description = "b374k webshell family detection"
        severity = "critical"
    strings:
        $b374k_title = "b374k" nocase
        $b374k_v1 = "b374k_config" nocase
        $b374k_v2 = "b374k_pass" nocase
        $b374k_feat1 = "proc_open" nocase
        $b374k_feat2 = "pcntl_exec" nocase
    condition:
        $b374k_title and 1 of ($b374k_v*, $b374k_feat*)
}

rule webshell_alfa {
    meta:
        author = "jabali-security"
        description = "Alfa Shell webshell family detection"
        severity = "critical"
    strings:
        $alfa_title = "Alfa Shell" nocase
        $alfa_v1 = "STARTER" nocase
        $alfa_v2 = "AlfaTeam" nocase
        $alfa_v3 = "Alfa_jCookie" nocase
        $alfa_v4 = "alfacgiapi" nocase
        $alfa_pass = "hashAlfa" nocase
    condition:
        $alfa_title or 2 of ($alfa_v*, $alfa_pass)
}

rule webshell_php_file_manager {
    meta:
        author = "jabali-security"
        description = "Unauthorized PHP file manager interface"
        severity = "medium"
    strings:
        $fm_upload = "move_uploaded_file" nocase
        $fm_delete = /\bunlink\s*\(\s*\$_(POST|REQUEST|GET)/ nocase
        $fm_mkdir = /\bmkdir\s*\(\s*\$_(POST|REQUEST|GET)/ nocase
        $fm_rename = /\brename\s*\(\s*\$_(POST|REQUEST|GET)/ nocase
        $fm_read = /file_get_contents\s*\(\s*\$_(POST|REQUEST|GET)/ nocase
        $fm_write = /file_put_contents\s*\(\s*\$_(POST|REQUEST|GET)/ nocase
        $fm_dir = "scandir" nocase
        $fm_perms = "chmod" nocase
    condition:
        4 of them
}

rule webshell_asp_execute {
    meta:
        author = "jabali-security"
        description = "ASP/ASPX webshell with code execution"
        severity = "high"
    strings:
        $asp_eval = "eval(Request" nocase
        $asp_execute = "Execute(Request" nocase
        $asp_wscript = "WScript.Shell" nocase
        $asp_cmd = "cmd.exe /c" nocase
        $aspx_process = "System.Diagnostics.Process" nocase
        $aspx_start = "Process.Start" nocase
        $asp_createobj = /CreateObject\s*\(\s*['"]Wscript\.Shell['"]\)/ nocase
        $asp_exec_request = /ExecuteGlobal\s*\(\s*Request/ nocase
    condition:
        any of them
}
