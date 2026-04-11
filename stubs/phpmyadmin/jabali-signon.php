<?php

/**
 * Jabali Panel SSO signon for phpMyAdmin
 *
 * Receives a one-time token, verifies it against the Jabali API,
 * then sets up a phpMyAdmin signon session and redirects.
 */

// Prevent caching
header('Cache-Control: no-cache, no-store, must-revalidate');
header('Pragma: no-cache');
header('Expires: 0');

$token = $_GET['token'] ?? '';
$db = $_GET['db'] ?? '';

// Validate token format: exactly 64 hex characters
if (! preg_match('/^[0-9a-f]{64}$/', $token)) {
    http_response_code(403);
    echo '<!DOCTYPE html><html><head><title>Access Denied</title></head><body>'
       .'<h1>Access Denied</h1>'
       .'<p>phpMyAdmin is only accessible through the Jabali Panel.</p>'
       .'<p><a href="/jabali-user/databases">Go to Databases</a></p>'
       .'</body></html>';
    exit;
}

// Verify token against Jabali API (localhost only)
// Use HTTPS with the Host header matching the server name, and skip cert verification
// since this is a localhost-only call and the panel may use a self-signed certificate.
$host = $_SERVER['HTTP_HOST'] ?? $_SERVER['SERVER_NAME'] ?? 'localhost';
$ch = curl_init();
curl_setopt_array($ch, [
    CURLOPT_URL => 'https://127.0.0.1:'.($_ENV['PANEL_PORT'] ?? '8443').'/api/phpmyadmin/verify-token',
    CURLOPT_POST => true,
    CURLOPT_POSTFIELDS => json_encode(['token' => $token]),
    CURLOPT_HTTPHEADER => [
        'Content-Type: application/json',
        'Accept: application/json',
        'Host: '.$host,
    ],
    CURLOPT_RETURNTRANSFER => true,
    CURLOPT_TIMEOUT => 10,
    CURLOPT_CONNECTTIMEOUT => 5,
    CURLOPT_SSL_VERIFYPEER => false,
    CURLOPT_SSL_VERIFYHOST => 0,
]);

$response = curl_exec($ch);
$httpCode = curl_getinfo($ch, CURLINFO_HTTP_CODE);
curl_close($ch);

if ($httpCode !== 200 || $response === false) {
    http_response_code(403);
    echo '<!DOCTYPE html><html><head><title>Access Denied</title></head><body>'
       .'<h1>Access Denied</h1>'
       .'<p>phpMyAdmin is only accessible through the Jabali Panel.</p>'
       .'<p><a href="/jabali-user/databases">Go to Databases</a></p>'
       .'</body></html>';
    exit;
}

$data = json_decode($response, true);
if (! is_array($data) || empty($data['username']) || empty($data['password'])) {
    http_response_code(403);
    echo '<!DOCTYPE html><html><head><title>Access Denied</title></head><body>'
       .'<h1>Access Denied</h1>'
       .'<p>phpMyAdmin is only accessible through the Jabali Panel.</p>'
       .'<p><a href="/jabali-user/databases">Go to Databases</a></p>'
       .'</body></html>';
    exit;
}

// Set up phpMyAdmin signon session
// Session name must match SignonSession in phpMyAdmin config
// Cookie path must be / so phpMyAdmin can read it from any path
session_name('jabali_phpmyadmin_signon');
session_set_cookie_params(['path' => '/', 'secure' => true, 'httponly' => true, 'samesite' => 'Lax']);
session_start();

$_SESSION['PMA_single_signon_user'] = $data['username'];
$_SESSION['PMA_single_signon_password'] = $data['password'];
$_SESSION['PMA_single_signon_HMAC_secret'] = random_bytes(32);

session_write_close();

// Auto-assign user to 'panel_users' group (restricts server-level menu items)
try {
    $passLine = file_get_contents('/etc/phpmyadmin/.pma_control_pass') ?: '';
    $pmaPass = trim(str_replace('PMA_PASS=', '', $passLine));
    $pdo = new PDO('mysql:host=localhost;dbname=phpmyadmin', 'pma', $pmaPass);
    $pdo->setAttribute(PDO::ATTR_ERRMODE, PDO::ERRMODE_EXCEPTION);
    $stmt = $pdo->prepare('INSERT IGNORE INTO pma__users (username, usergroup) VALUES (?, ?)');
    $stmt->execute([$data['username'], 'panel_users']);
} catch (Throwable $e) {
    // Non-fatal: SSO still works, just without menu restrictions
}

// Use database from API response, fall back to query param
$database = $data['database'] ?? $db;

// Redirect to phpMyAdmin
$redirectUrl = '/phpmyadmin/index.php';
if (! empty($database)) {
    $redirectUrl .= '?db='.urlencode($database);
}

header('Location: '.$redirectUrl);
exit;
