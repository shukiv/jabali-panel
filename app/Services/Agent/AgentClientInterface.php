<?php

declare(strict_types=1);

namespace App\Services\Agent;

interface AgentClientInterface
{
    public function send(string $action, array $data = []): array;
}
