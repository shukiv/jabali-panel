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
$cfg['Servers'][$i]['LogoutURL'] = '/jabali-panel/databases';
$cfg['Servers'][$i]['host'] = 'localhost';

// phpMyAdmin configuration storage (enables user groups for menu restrictions)
$cfg['Servers'][$i]['pmadb'] = 'phpmyadmin';
$cfg['Servers'][$i]['controluser'] = 'pma';
$cfg['Servers'][$i]['controlpass'] = '%%PMA_CONTROL_PASS%%';
$cfg['Servers'][$i]['users'] = 'pma__users';
$cfg['Servers'][$i]['usergroups'] = 'pma__usergroups';
$cfg['Servers'][$i]['bookmarktable'] = 'pma__bookmark';
$cfg['Servers'][$i]['relation'] = 'pma__relation';
$cfg['Servers'][$i]['table_info'] = 'pma__table_info';
$cfg['Servers'][$i]['table_uiprefs'] = 'pma__table_uiprefs';
$cfg['Servers'][$i]['navigationhiding'] = 'pma__navigationhiding';
$cfg['Servers'][$i]['designer_settings'] = 'pma__designer_settings';
$cfg['Servers'][$i]['export_templates'] = 'pma__export_templates';

// Restrict access — no login form, signon only
$cfg['Servers'][$i]['AllowNoPassword'] = false;
$cfg['Servers'][$i]['AllowRoot'] = false;

// Hide server-level features (users manage these through the panel)
$cfg['ShowServerInfo'] = false;
$cfg['ShowPhpInfo'] = false;
$cfg['ShowCreateDb'] = false;
$cfg['ShowChgPassword'] = false;

// Hardening
$cfg['AllowUserDropDatabase'] = false;
$cfg['Servers'][$i]['hide_db'] = '^(mysql|information_schema|performance_schema|sys|phpmyadmin)$';
