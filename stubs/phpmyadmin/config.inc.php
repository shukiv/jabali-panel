<?php

/**
 * Jabali Panel phpMyAdmin configuration
 *
 * This file is loaded from /etc/phpmyadmin/conf.d/jabali.inc.php
 * by the Debian phpMyAdmin package.
 */

// Blowfish secret for cookie encryption (generated during install)
$cfg['blowfish_secret'] = '%%BLOWFISH_SECRET%%';

// Server configuration
$i = 1;
$cfg['Servers'][$i]['auth_type'] = 'signon';
$cfg['Servers'][$i]['SignonSession'] = 'jabali_phpmyadmin_signon';
$cfg['Servers'][$i]['SignonURL'] = '/phpmyadmin/jabali-signon.php';
$cfg['Servers'][$i]['LogoutURL'] = '/jabali-user/databases';
$cfg['Servers'][$i]['host'] = 'localhost';

// Disable phpMyAdmin configuration storage (not needed)
$cfg['Servers'][$i]['pmadb'] = '';

// Restrict access — no login form, signon only
$cfg['Servers'][$i]['AllowNoPassword'] = false;
