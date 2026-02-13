<?php

declare(strict_types=1);

namespace App\Http\Controllers;

use App\Models\EmailDomain;
use Illuminate\Http\Request;
use Illuminate\Http\Response;

/**
 * AutoDiscover Controller for Microsoft Outlook and Exchange clients
 *
 * Handles the autodiscover protocol to automatically configure email clients.
 * Supports both GET and POST requests for autodiscover.xml
 */
class AutoDiscoverController extends Controller
{
    /**
     * Handle autodiscover request (POST with XML body)
     */
    public function discover(Request $request): Response
    {
        $xml = $request->getContent();

        // Parse email address from request
        $email = $this->normalizeEmail($this->extractEmailFromRequest($xml));

        if (! $email) {
            return $this->errorResponse('No email address provided');
        }

        // Extract domain from email
        $parts = explode('@', $email);
        if (count($parts) !== 2) {
            return $this->errorResponse('Invalid email address');
        }

        $domain = $parts[1];

        // Check if domain is managed by our mail server
        $emailDomain = EmailDomain::whereHas('domain', function ($query) use ($domain) {
            $query->where('domain_name', $domain);
        })->where('is_active', true)->first();

        if (! $emailDomain) {
            return $this->errorResponse('Domain not configured for email');
        }

        // Get mail server hostname
        $mailServer = $this->getMailServer($emailDomain);

        return $this->autodiscoverResponse($email, $mailServer);
    }

    /**
     * Handle GET request for autodiscover (some clients use this)
     */
    public function discoverGet(Request $request): Response
    {
        $email = $this->normalizeEmail($request->query('email') ?? $request->query('Email'));

        if (! $email) {
            return response('Email parameter required', 400);
        }

        $parts = explode('@', $email);
        if (count($parts) !== 2) {
            return response('Invalid email address', 400);
        }

        $domain = $parts[1];

        $emailDomain = EmailDomain::whereHas('domain', function ($query) use ($domain) {
            $query->where('domain_name', $domain);
        })->where('is_active', true)->first();

        if (! $emailDomain) {
            return response('Domain not configured', 404);
        }

        $mailServer = $this->getMailServer($emailDomain);

        return $this->autodiscoverResponse($email, $mailServer);
    }

    /**
     * Extract email from autodiscover XML request
     */
    private function extractEmailFromRequest(string $xml): ?string
    {
        if (empty($xml)) {
            return null;
        }

        // Try to parse XML
        libxml_use_internal_errors(true);
        $doc = simplexml_load_string($xml, 'SimpleXMLElement', LIBXML_NONET);

        if ($doc === false) {
            return null;
        }

        // Register namespaces
        $doc->registerXPathNamespace('a', 'http://schemas.microsoft.com/exchange/autodiscover/outlook/requestschema/2006');

        // Try different XPath expressions
        $results = $doc->xpath('//a:EMailAddress');
        if (! empty($results)) {
            return trim((string) $results[0]);
        }

        // Try without namespace
        $results = $doc->xpath('//EMailAddress');
        if (! empty($results)) {
            return trim((string) $results[0]);
        }

        // Try AcceptableResponseSchema pattern
        if (isset($doc->Request->EMailAddress)) {
            return trim((string) $doc->Request->EMailAddress);
        }

        return null;
    }

    /**
     * Get mail server hostname for the domain
     */
    private function getMailServer(EmailDomain $emailDomain): string
    {
        // Use setting or fall back to domain-based hostname
        $hostname = \App\Models\Setting::get('mail_hostname');

        if ($hostname) {
            return $hostname;
        }

        return 'mail.'.$emailDomain->domain->domain_name;
    }

    /**
     * Generate autodiscover XML response
     */
    private function autodiscoverResponse(string $email, string $mailServer): Response
    {
        $displayName = explode('@', $email)[0];
        $escapedEmail = $this->escapeXml($email);
        $escapedServer = $this->escapeXml($mailServer);
        $escapedDisplay = $this->escapeXml($displayName);

        $xml = <<<XML
<?xml version="1.0" encoding="utf-8"?>
<Autodiscover xmlns="http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006">
    <Response xmlns="http://schemas.microsoft.com/exchange/autodiscover/outlook/responseschema/2006a">
        <Account>
            <AccountType>email</AccountType>
            <Action>settings</Action>
            <Protocol>
                <Type>IMAP</Type>
                <Server>{$escapedServer}</Server>
                <Port>993</Port>
                <SSL>on</SSL>
                <SPA>off</SPA>
                <AuthRequired>on</AuthRequired>
                <LoginName>{$escapedEmail}</LoginName>
            </Protocol>
            <Protocol>
                <Type>SMTP</Type>
                <Server>{$escapedServer}</Server>
                <Port>587</Port>
                <SSL>on</SSL>
                <Encryption>TLS</Encryption>
                <SPA>off</SPA>
                <AuthRequired>on</AuthRequired>
                <LoginName>{$escapedEmail}</LoginName>
            </Protocol>
            <Protocol>
                <Type>POP3</Type>
                <Server>{$escapedServer}</Server>
                <Port>995</Port>
                <SSL>on</SSL>
                <SPA>off</SPA>
                <AuthRequired>on</AuthRequired>
                <LoginName>{$escapedEmail}</LoginName>
            </Protocol>
        </Account>
    </Response>
</Autodiscover>
XML;

        return response($xml, 200)
            ->header('Content-Type', 'application/xml; charset=utf-8');
    }

    /**
     * Generate error response
     */
    private function errorResponse(string $message): Response
    {
        $escapedMessage = $this->escapeXml($message);

        $xml = <<<XML
<?xml version="1.0" encoding="utf-8"?>
<Autodiscover xmlns="http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006">
    <Response>
        <Error Time="{$this->getTimestamp()}" Id="1">
            <ErrorCode>600</ErrorCode>
            <Message>{$escapedMessage}</Message>
            <DebugData />
        </Error>
    </Response>
</Autodiscover>
XML;

        return response($xml, 200)
            ->header('Content-Type', 'application/xml; charset=utf-8');
    }

    private function getTimestamp(): string
    {
        return date('H:i:s.v');
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

    private function escapeXml(string $value): string
    {
        return htmlspecialchars($value, ENT_XML1 | ENT_QUOTES, 'UTF-8');
    }
}
