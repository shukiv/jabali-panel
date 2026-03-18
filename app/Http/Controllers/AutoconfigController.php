<?php

declare(strict_types=1);

namespace App\Http\Controllers;

use App\Models\DnsSetting;
use App\Models\EmailDomain;
use Illuminate\Http\Request;
use Illuminate\Http\Response;

/**
 * Autoconfig Controller for Mozilla Thunderbird and other clients
 *
 * Implements the Mozilla Autoconfig protocol for automatic email configuration.
 * Also provides mobile configuration profiles for iOS devices.
 */
class AutoconfigController extends Controller
{
    /**
     * Handle autoconfig request (Mozilla Thunderbird style)
     * URL: /mail/config-v1.1.xml?emailaddress=user@example.com
     * URL: /.well-known/autoconfig/mail/config-v1.1.xml?emailaddress=user@example.com
     */
    public function config(Request $request): Response
    {
        $email = $this->normalizeEmail($request->query('emailaddress'));

        if ($email) {
            $parts = explode('@', $email);
            if (count($parts) !== 2) {
                return response('Invalid email address', 400);
            }
            $domain = $parts[1];
        } else {
            // Extract domain from Host header (strip autoconfig. prefix)
            $host = $request->getHost();
            $domain = preg_replace('/^autoconfig\./i', '', $host);
            if (! $domain || ! $this->isValidDomain($domain)) {
                return response('Email parameter required', 400);
            }
        }

        // Check if domain is managed
        $emailDomain = EmailDomain::whereHas('domain', function ($query) use ($domain) {
            $query->where('domain', $domain);
        })->where('is_active', true)->first();

        if (! $emailDomain) {
            return response('Domain not configured', 404);
        }

        $mailServer = $this->getMailServer($emailDomain);
        $displayName = DnsSetting::get('webmail_product_name', 'Jabali Mail');

        return $this->autoconfigResponse($domain, $mailServer, $displayName);
    }

    /**
     * Handle autoconfig for specific domain
     * URL: /autoconfig/domain.com/config-v1.1.xml
     */
    public function configForDomain(Request $request, string $domain): Response
    {
        if (! $this->isValidDomain($domain)) {
            return response('Invalid domain', 400);
        }

        $emailDomain = EmailDomain::whereHas('domain', function ($query) use ($domain) {
            $query->where('domain', $domain);
        })->where('is_active', true)->first();

        if (! $emailDomain) {
            return response('Domain not configured', 404);
        }

        $mailServer = $this->getMailServer($emailDomain);
        $displayName = DnsSetting::get('webmail_product_name', 'Jabali Mail');

        return $this->autoconfigResponse($domain, $mailServer, $displayName);
    }

    /**
     * Generate iOS/macOS mobile configuration profile
     * URL: /mail/profile/{domain}
     */
    public function mobileProfile(Request $request, string $domain): Response
    {
        if (! $this->isValidDomain($domain)) {
            return response('Invalid domain', 400);
        }

        $email = $this->normalizeEmail($request->query('email'));

        if (! $email) {
            return response('Email parameter required', 400);
        }

        $emailDomainName = substr(strrchr($email, '@') ?: '', 1);
        if ($emailDomainName !== $domain) {
            return response('Email does not match domain', 400);
        }

        $emailDomain = EmailDomain::whereHas('domain', function ($query) use ($domain) {
            $query->where('domain', $domain);
        })->where('is_active', true)->first();

        if (! $emailDomain) {
            return response('Domain not configured', 404);
        }

        $mailServer = $this->getMailServer($emailDomain);
        $displayName = DnsSetting::get('webmail_product_name', 'Jabali Mail');
        $escapedDomain = $this->escapeXml($domain);
        $escapedEmail = $this->escapeXml($email);
        $escapedMailServer = $this->escapeXml($mailServer);
        $escapedDisplayName = $this->escapeXml($displayName);
        $uuid = $this->generateUuid();
        $payloadUuid = $this->generateUuid();

        $profile = <<<MOBILECONFIG
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>PayloadContent</key>
    <array>
        <dict>
            <key>EmailAccountDescription</key>
            <string>{$escapedDomain} Email</string>
            <key>EmailAccountName</key>
            <string>{$escapedDisplayName}</string>
            <key>EmailAccountType</key>
            <string>EmailTypeIMAP</string>
            <key>EmailAddress</key>
            <string>{$escapedEmail}</string>
            <key>IncomingMailServerAuthentication</key>
            <string>EmailAuthPassword</string>
            <key>IncomingMailServerHostName</key>
            <string>{$escapedMailServer}</string>
            <key>IncomingMailServerPortNumber</key>
            <integer>993</integer>
            <key>IncomingMailServerUseSSL</key>
            <true/>
            <key>IncomingMailServerUsername</key>
            <string>{$escapedEmail}</string>
            <key>OutgoingMailServerAuthentication</key>
            <string>EmailAuthPassword</string>
            <key>OutgoingMailServerHostName</key>
            <string>{$escapedMailServer}</string>
            <key>OutgoingMailServerPortNumber</key>
            <integer>465</integer>
            <key>OutgoingMailServerUseSSL</key>
            <true/>
            <key>OutgoingMailServerUsername</key>
            <string>{$escapedEmail}</string>
            <key>OutgoingPasswordSameAsIncomingPassword</key>
            <true/>
            <key>PayloadDescription</key>
            <string>Email account configuration for {$escapedDomain}</string>
            <key>PayloadDisplayName</key>
            <string>{$escapedDomain} Email</string>
            <key>PayloadIdentifier</key>
            <string>com.jabali.email.{$escapedDomain}</string>
            <key>PayloadType</key>
            <string>com.apple.mail.managed</string>
            <key>PayloadUUID</key>
            <string>{$payloadUuid}</string>
            <key>PayloadVersion</key>
            <integer>1</integer>
            <key>PreventAppSheet</key>
            <false/>
            <key>PreventMove</key>
            <false/>
            <key>SMIMEEnabled</key>
            <false/>
        </dict>
    </array>
    <key>PayloadDescription</key>
    <string>Email configuration profile for {$escapedDomain}</string>
    <key>PayloadDisplayName</key>
    <string>{$escapedDisplayName} - {$escapedDomain}</string>
    <key>PayloadIdentifier</key>
    <string>com.jabali.mailconfig.{$escapedDomain}</string>
    <key>PayloadOrganization</key>
    <string>{$escapedDisplayName}</string>
    <key>PayloadRemovalDisallowed</key>
    <false/>
    <key>PayloadType</key>
    <string>Configuration</string>
    <key>PayloadUUID</key>
    <string>{$uuid}</string>
    <key>PayloadVersion</key>
    <integer>1</integer>
</dict>
</plist>
MOBILECONFIG;

        return response($profile, 200)
            ->header('Content-Type', 'application/x-apple-aspen-config')
            ->header('Content-Disposition', 'attachment; filename="email-config.mobileconfig"');
    }

    /**
     * Get mail server hostname
     */
    private function getMailServer(EmailDomain $emailDomain): string
    {
        $hostname = DnsSetting::get('mail_hostname');

        if ($hostname) {
            return $hostname;
        }

        return 'mail.'.$emailDomain->domain_name;
    }

    /**
     * Generate autoconfig XML response
     */
    private function autoconfigResponse(string $domain, string $mailServer, string $displayName): Response
    {
        $escapedDomain = $this->escapeXml($domain);
        $escapedMailServer = $this->escapeXml($mailServer);
        $escapedDisplayName = $this->escapeXml($displayName);

        $jmapSection = '';
        if (config('jabali.mail_backend') === 'stalwart') {
            $jmapSection = <<<JMAP
        <incomingServer type="jmap">
            <hostname>{$escapedMailServer}</hostname>
            <port>443</port>
            <socketType>SSL</socketType>
            <authentication>password-cleartext</authentication>
            <username>%EMAILADDRESS%</username>
        </incomingServer>
JMAP;
        }

        $xml = <<<XML
<?xml version="1.0" encoding="UTF-8"?>
<clientConfig version="1.1">
    <emailProvider id="{$escapedDomain}">
        <domain>{$escapedDomain}</domain>
        <displayName>{$escapedDisplayName}</displayName>
        <displayShortName>{$escapedDisplayName}</displayShortName>
        <incomingServer type="imap">
            <hostname>{$escapedMailServer}</hostname>
            <port>993</port>
            <socketType>SSL</socketType>
            <authentication>password-cleartext</authentication>
            <username>%EMAILADDRESS%</username>
        </incomingServer>
        <incomingServer type="pop3">
            <hostname>{$escapedMailServer}</hostname>
            <port>995</port>
            <socketType>SSL</socketType>
            <authentication>password-cleartext</authentication>
            <username>%EMAILADDRESS%</username>
        </incomingServer>
{$jmapSection}
        <outgoingServer type="smtp">
            <hostname>{$escapedMailServer}</hostname>
            <port>465</port>
            <socketType>SSL</socketType>
            <authentication>password-cleartext</authentication>
            <username>%EMAILADDRESS%</username>
        </outgoingServer>
        <outgoingServer type="smtp">
            <hostname>{$escapedMailServer}</hostname>
            <port>587</port>
            <socketType>STARTTLS</socketType>
            <authentication>password-cleartext</authentication>
            <username>%EMAILADDRESS%</username>
        </outgoingServer>
        <documentation url="https://{$escapedDomain}/webmail">
            <descr lang="en">Webmail access</descr>
        </documentation>
    </emailProvider>
</clientConfig>
XML;

        return response($xml, 200)
            ->header('Content-Type', 'application/xml; charset=utf-8');
    }

    private function normalizeEmail(?string $email): ?string
    {
        if ($email === null) {
            return null;
        }

        $email = trim($email);

        if ($email === '' || ! filter_var($email, FILTER_VALIDATE_EMAIL)) {
            return null;
        }

        return $email;
    }

    private function isValidDomain(string $domain): bool
    {
        return filter_var($domain, FILTER_VALIDATE_DOMAIN, FILTER_FLAG_HOSTNAME) !== false;
    }

    private function escapeXml(string $value): string
    {
        return htmlspecialchars($value, ENT_XML1 | ENT_QUOTES, 'UTF-8');
    }

    /**
     * Generate UUID v4
     */
    private function generateUuid(): string
    {
        return sprintf(
            '%04x%04x-%04x-%04x-%04x-%04x%04x%04x',
            mt_rand(0, 0xFFFF),
            mt_rand(0, 0xFFFF),
            mt_rand(0, 0xFFFF),
            mt_rand(0, 0x0FFF) | 0x4000,
            mt_rand(0, 0x3FFF) | 0x8000,
            mt_rand(0, 0xFFFF),
            mt_rand(0, 0xFFFF),
            mt_rand(0, 0xFFFF)
        );
    }
}
