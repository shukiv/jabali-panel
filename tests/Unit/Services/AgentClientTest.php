<?php

namespace Tests\Unit\Services;

use App\Services\Agent\AgentClient;
use Tests\TestCase;

class AgentClientTest extends TestCase
{
    public function test_agent_client_can_be_instantiated(): void
    {
        $this->assertTrue(class_exists(AgentClient::class));
    }

    public function test_ping_returns_pong(): void
    {
        // Skip if agent socket is not available
        if (!file_exists('/var/run/jabali/agent.sock')) {
            $this->markTestSkipped('Agent socket not available');
        }

        $client = new AgentClient();
        $response = $client->send('ping');

        $this->assertTrue($response['success']);
        $this->assertEquals('pong', $response['message']);
    }
}
