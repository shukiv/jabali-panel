rule uploader_standalone_shell {
    meta:
        author = "jabali-security"
        description = "Standalone PHP file upload shell interface"
        severity = "high"
    strings:
        $upload_form = "enctype=\"multipart/form-data\"" nocase
        $file_input = "<input" nocase
        $type_file = "type=\"file\"" nocase
        $move_uploaded = "move_uploaded_file" nocase
        $tmp_name = "$_FILES" nocase
    condition:
        $move_uploaded and $tmp_name and ($upload_form or ($file_input and $type_file)) and filesize < 50KB
}

rule uploader_dropper_wget_curl {
    meta:
        author = "jabali-security"
        description = "Dropper script that downloads and deploys remote payloads"
        severity = "critical"
    strings:
        $wget_exec = /wget\s+.+\s+-O\s+\S+\s*;?\s*(chmod|php|bash|sh|perl|python)/ nocase
        $curl_exec = /curl\s+.+\s+-o\s+\S+\s*;?\s*(chmod|php|bash|sh|perl|python)/ nocase
        $wget_pipe = /wget\s+-q?\s+\S+\s+-O\s*-\s*\|\s*(ba)?sh/ nocase
        $curl_pipe = /curl\s+-s?\s+\S+\s*\|\s*(ba)?sh/ nocase
        $php_dl1 = /file_put_contents\s*\(.+,\s*file_get_contents\s*\(\s*['"]https?:\/\// nocase
        $php_dl2 = /fwrite\s*\(.+fopen\s*\(\s*['"]https?:\/\// nocase
        $php_dl3 = /copy\s*\(\s*['"]https?:\/\/.*['"]\s*,/ nocase
    condition:
        any of them
}

rule uploader_php_dropper_eval {
    meta:
        author = "jabali-security"
        description = "PHP dropper that fetches and evaluates remote code"
        severity = "critical"
    strings:
        $fetch_eval1 = /eval\s*\(\s*file_get_contents\s*\(\s*['"]https?:\/\// nocase
        $fetch_eval2 = /eval\s*\(\s*\$[a-zA-Z_]+\s*\)\s*;/ nocase
        $curl_init = "curl_init(" nocase
        $curl_exec_fn = "curl_exec(" nocase
        $eval_call = "eval(" nocase
        $b64 = "base64_decode(" nocase
    condition:
        $fetch_eval1 or ($curl_init and $curl_exec_fn and $eval_call) or ($fetch_eval2 and $b64 and $curl_init)
}

rule uploader_mass_defacement {
    meta:
        author = "jabali-security"
        description = "Mass defacement tool that modifies multiple website index files"
        severity = "critical"
    strings:
        $glob_index = /glob\s*\(\s*['"].*index\.\(php\|html?\)['"]/ nocase
        $scandir = "scandir(" nocase
        $recursive = "RecursiveDirectoryIterator" nocase
        $index_write = /file_put_contents\s*\(\s*.*index\.(php|html?)/ nocase
        $fwrite_index = /fopen\s*\(\s*.*index\.(php|html?).*['"]w['"]/ nocase
        $hacked_by = "Hacked By" nocase
        $defaced = "Defaced" nocase
        $owned_by = "Owned by" nocase
        $mass_deface = "mass deface" nocase
    condition:
        ($scandir or $recursive or $glob_index) and ($index_write or $fwrite_index) and 1 of ($hacked_by, $defaced, $owned_by, $mass_deface)
}

rule uploader_spam_mailer {
    meta:
        author = "jabali-security"
        description = "PHP spam mailer script for sending bulk email"
        severity = "high"
    strings:
        $mail_func = "mail(" nocase
        $bcc_header = "Bcc:" nocase
        $mailer_title = "mailer" nocase
        $send_mail = "send mail" nocase
        $mass_mail = "mass mail" nocase
        $email_list = "email list" nocase
        $letter = "letter" nocase
        $inbox = "inbox" nocase
        $spam = "spam" nocase
        $phpmailer_exploit = /\$_(POST|GET|REQUEST)\s*\[\s*['"]to['"]/ nocase
    condition:
        ($mail_func and ($bcc_header or $phpmailer_exploit)) and 2 of ($mailer_title, $send_mail, $mass_mail, $email_list, $letter, $inbox, $spam)
}

rule uploader_webshell_deployer {
    meta:
        author = "jabali-security"
        description = "Script that writes webshell code to disk"
        severity = "critical"
    strings:
        $write_func = "file_put_contents(" nocase
        $fwrite = "fwrite(" nocase
        $shell_code1 = "eval($_POST" nocase
        $shell_code2 = "eval($_REQUEST" nocase
        $shell_code3 = "system($_GET" nocase
        $shell_code4 = "base64_decode($_POST" nocase
        $b64_payload = /base64_decode\s*\(\s*['"][A-Za-z0-9+\/=]{50,}['"]\s*\)/ nocase
    condition:
        ($write_func or $fwrite) and (any of ($shell_code*) or $b64_payload)
}

rule uploader_multi_site_inject {
    meta:
        author = "jabali-security"
        description = "Tool that injects code across multiple sites on shared hosting"
        severity = "critical"
    strings:
        $home_dir = "/home/" nocase
        $public_html = "public_html" nocase
        $www = "www" nocase
        $glob_home = /glob\s*\(\s*['"]\/home\/\*/ nocase
        $scandir_home = /scandir\s*\(\s*['"]\/home['"]/ nocase
        $dir_iter = "DirectoryIterator" nocase
        $file_write = "file_put_contents" nocase
        $fwrite = "fwrite" nocase
        $append = /\bFILE_APPEND\b/ nocase
    condition:
        ($glob_home or $scandir_home or ($dir_iter and $home_dir)) and ($public_html or $www) and ($file_write or $fwrite or $append)
}
