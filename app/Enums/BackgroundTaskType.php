<?php

declare(strict_types=1);

namespace App\Enums;

enum BackgroundTaskType: string
{
    case Backup = 'backup';
    case SslIssue = 'ssl_issue';
    case SslRenew = 'ssl_renew';
    case UserCreate = 'user_create';
    case UserDelete = 'user_delete';
    case AddonInstall = 'addon_install';
    case AddonUninstall = 'addon_uninstall';
    case GitDeploy = 'git_deploy';
    case CpanelRestore = 'cpanel_restore';
    case WhmMigration = 'whm_migration';
    case ImapSync = 'imap_sync';

    public function label(): string
    {
        return match ($this) {
            self::Backup => __('Backup'),
            self::SslIssue => __('SSL Issue'),
            self::SslRenew => __('SSL Renew'),
            self::UserCreate => __('User Create'),
            self::UserDelete => __('User Delete'),
            self::AddonInstall => __('Addon Install'),
            self::AddonUninstall => __('Addon Uninstall'),
            self::GitDeploy => __('Git Deploy'),
            self::CpanelRestore => __('cPanel Restore'),
            self::WhmMigration => __('WHM Migration'),
            self::ImapSync => __('IMAP Sync'),
        };
    }

    /**
     * Default retry policy for this task type.
     * Backups should never auto-retry (idempotency concerns).
     * SSL can retry on transient network failures.
     */
    public function defaultMaxRetries(): int
    {
        return match ($this) {
            self::SslIssue, self::SslRenew, self::ImapSync => 3,
            default => 0,
        };
    }
}
