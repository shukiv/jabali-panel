<?php

declare(strict_types=1);

namespace App\Services\System;

use App\Models\User;
use App\Services\Agent\AgentClient;
use Exception;

class LinuxUserService
{
    protected AgentClient $agent;

    public function __construct(?AgentClient $agent = null)
    {
        $this->agent = $agent ?? new AgentClient;
    }

    /**
     * Create a Linux system user
     */
    public function createUser(User $user, ?string $password = null): bool
    {
        $response = $this->agent->createUser($user->username, $password);

        if ($response['success'] ?? false) {
            $user->update([
                'home_directory' => $response['home_directory'] ?? "/home/{$user->username}",
            ]);

            return true;
        }

        throw new Exception($response['error'] ?? 'Failed to create user');
    }

    /**
     * Delete a Linux system user
     *
     * @param  array<string>  $domains
     * @return array<string, mixed>
     */
    public function deleteUser(string $username, bool $removeHome = false, array $domains = []): array
    {
        return $this->agent->deleteUser($username, $removeHome, $domains);
    }

    /**
     * Check if a Linux user exists
     */
    public function userExists(string $username): bool
    {
        return $this->agent->userExists($username);
    }

    /**
     * Set password for Linux user
     */
    public function setPassword(string $username, string $password): bool
    {
        $response = $this->agent->setUserPassword($username, $password);

        return $response['success'] ?? false;
    }

    /**
     * Validate username format
     */
    public static function isValidUsername(string $username): bool
    {
        return preg_match('/^[a-z][a-z0-9_]{0,31}$/', $username) === 1;
    }
}
