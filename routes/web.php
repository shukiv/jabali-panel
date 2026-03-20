<?php

use App\Http\Controllers\AutoconfigController;
use App\Http\Controllers\AutoDiscoverController;
use App\Http\Controllers\BackupDownloadController;
use App\Http\Controllers\ImpersonationController;
use App\Http\Controllers\LanguageController;
use Illuminate\Support\Facades\Route;

Route::get('/', function () {
    return redirect('/jabali-panel');
});

// Override health check to avoid CDN-hosted Tailwind (Cross-Domain JS inclusion)
Route::get('/up', function () {
    return response('<html><body style="font-family:sans-serif;display:flex;justify-content:center;align-items:center;height:100vh;margin:0"><div><div style="display:flex;align-items:center;gap:8px"><span style="width:12px;height:12px;border-radius:50%;background:#22c55e;display:inline-block"></span><strong>Application up</strong></div></div></body></html>', 200)
        ->header('Content-Type', 'text/html');
});

Route::get('/login', function () {
    return redirect('/jabali-panel/login');
})->name('login');

Route::get('/register', function () {
    return redirect('/jabali-panel/register');
});

// Backward-compatible alias: the "System Updates" page lives at /jabali-admin/server-updates.
Route::redirect('/jabali-admin/system-updates', '/jabali-admin/server-updates');

/*
|--------------------------------------------------------------------------
| Two-Factor Authentication Challenge
|--------------------------------------------------------------------------
*/

Route::get('/jabali-panel/two-factor-challenge', \App\Filament\Jabali\Pages\Auth\TwoFactorChallenge::class)
    ->middleware('web')
    ->name('filament.jabali.auth.two-factor-challenge');

Route::get('/jabali-admin/two-factor-challenge', \App\Filament\Admin\Pages\Auth\TwoFactorChallenge::class)
    ->middleware('web')
    ->name('filament.admin.auth.two-factor-challenge');

/*
|--------------------------------------------------------------------------
| Email Auto-Configuration Routes
|--------------------------------------------------------------------------
|
| These routes handle automatic email client configuration for:
| - Microsoft Outlook (Autodiscover)
| - Mozilla Thunderbird (Autoconfig)
| - iOS/macOS Mail (Mobile Config Profile)
|
*/

// Microsoft Autodiscover (Outlook, Windows Mail, mobile Exchange clients)
Route::match(['get', 'post'], '/autodiscover/autodiscover.xml', [AutoDiscoverController::class, 'discover']);
Route::match(['get', 'post'], '/Autodiscover/Autodiscover.xml', [AutoDiscoverController::class, 'discover']);
Route::match(['get', 'post'], '/AutoDiscover/AutoDiscover.xml', [AutoDiscoverController::class, 'discover']);

// Mozilla Autoconfig (Thunderbird, K-9 Mail, other standards-compliant clients)
Route::get('/mail/config-v1.1.xml', [AutoconfigController::class, 'config']);
Route::get('/.well-known/autoconfig/mail/config-v1.1.xml', [AutoconfigController::class, 'config']);
Route::get('/autoconfig/{domain}/config-v1.1.xml', [AutoconfigController::class, 'configForDomain']);

// iOS/macOS Mobile Configuration Profile
Route::get('/mail/profile/{domain}', [AutoconfigController::class, 'mobileProfile']);

/*
|--------------------------------------------------------------------------
| Admin Impersonation
|--------------------------------------------------------------------------
|
| Allows administrators to login as a user using a one-time token.
| Opens in a new tab with a separate session.
|
*/

Route::get('/impersonate/start/{user}', [ImpersonationController::class, 'start'])
    ->middleware('auth:admin')
    ->name('impersonate.start');

Route::post('/impersonate/stop', [ImpersonationController::class, 'stop'])
    ->middleware('auth:web')
    ->name('impersonate.stop');

Route::get('/impersonate/{token}', [ImpersonationController::class, 'impersonate'])
    ->name('impersonate');

/*
|--------------------------------------------------------------------------
| User Panel Backup Download
|--------------------------------------------------------------------------
*/

Route::get('/jabali-panel/backup-download', [BackupDownloadController::class, 'download'])
    ->middleware(['web', 'auth'])
    ->name('filament.jabali.pages.backup-download');

/*
|--------------------------------------------------------------------------
| Admin Panel Backup Download
|--------------------------------------------------------------------------
*/

Route::get('/jabali-admin/backup-download', [BackupDownloadController::class, 'adminDownload'])
    ->middleware(['web', 'auth:admin'])
    ->name('filament.admin.pages.backup-download');

/*
|--------------------------------------------------------------------------
| Language Switcher
|--------------------------------------------------------------------------
*/

Route::get('/language/{locale}', [LanguageController::class, 'switch'])
    ->name('language.switch');

/*
|--------------------------------------------------------------------------
| Webmail SSO
|--------------------------------------------------------------------------
|
| Generates SSO token and redirects to webmail.
| Supports Bulwark (Stalwart backend) and Roundcube (legacy backend).
| Opens in new tab, so uses signed URL for security.
|
*/

Route::get('/webmail-sso/{mailbox}', function (\App\Models\Mailbox $mailbox) {
    // Verify user owns this mailbox
    if ($mailbox->user_id !== auth()->id()) {
        abort(403);
    }

    $ssoDir = '/var/lib/jabali/sso-tokens';
    if (! is_dir($ssoDir) && ! @mkdir($ssoDir, 0700, true)) {
        abort(500, 'SSO token directory could not be created. Run: mkdir -p /var/lib/jabali/sso-tokens && chown www-data:www-data /var/lib/jabali/sso-tokens && chmod 700 /var/lib/jabali/sso-tokens');
    }

    if (config('jabali.mail_backend') === 'stalwart') {
        // Bulwark webmail — SSO via token file
        $webmailUrl = \App\Models\DnsSetting::get('webmail_url', '/webmail/');
        $token = bin2hex(random_bytes(32));
        $tokenData = [
            'email' => $mailbox->email,
            'expires' => time() + 60,
        ];

        if ($mailbox->plain_password) {
            $tokenData['password'] = $mailbox->plain_password;
            $tokenData['use_direct'] = true;
        }

        $ssoFile = $ssoDir.'/bulwark_sso_'.$token;
        file_put_contents($ssoFile, json_encode($tokenData), LOCK_EX);
        chmod($ssoFile, 0600);

        // Redirect to webmail on the mailbox's own domain
        $domain = $mailbox->emailDomain?->domain?->domain ?? '';
        $ssoUrl = $domain
            ? "https://{$domain}/webmail/api/auth/sso?token={$token}"
            : "/webmail/api/auth/sso?token={$token}";

        return redirect($ssoUrl);
    }

    // Legacy Roundcube SSO
    $password = $mailbox->plain_password;
    if ($password) {
        // Create SSO token for auto-login
        $token = bin2hex(random_bytes(32));
        $tokenData = [
            'email' => $mailbox->email,
            'password' => $password,
            'expires' => time() + 300,
        ];

        $ssoFile = $ssoDir.'/roundcube_sso_'.$token;
        file_put_contents($ssoFile, json_encode($tokenData), LOCK_EX);
        chmod($ssoFile, 0600);

        return redirect('/webmail/jabali-sso.php?token='.$token);
    }

    $hasPasswordHash = ! empty($mailbox->password_hash);
    $masterConfigPath = '/etc/jabali/roundcube-sso.conf';
    if ($hasPasswordHash && file_exists($masterConfigPath)) {
        $token = bin2hex(random_bytes(32));
        $tokenData = [
            'login_as' => $mailbox->email,
            'use_master' => true,
            'expires' => time() + 300,
        ];

        $ssoFile = $ssoDir.'/roundcube_sso_'.$token;
        file_put_contents($ssoFile, json_encode($tokenData), LOCK_EX);
        chmod($ssoFile, 0600);

        return redirect('/webmail/jabali-sso.php?token='.$token);
    }

    // No stored password - show message about needing to reset password first
    // This happens after restore when password_encrypted isn't set
    return response()->view('webmail-password-required', [
        'email' => $mailbox->email,
        'mailbox_id' => $mailbox->id,
        'has_password_hash' => $hasPasswordHash,
    ]);
})->middleware(['web', 'auth'])->name('webmail.sso');
