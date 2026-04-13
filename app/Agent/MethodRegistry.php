<?php

declare(strict_types=1);

namespace App\Agent;

use InvalidArgumentException;

/**
 * Schema-driven allowlist for agent socket methods.
 *
 * The registry is the capability layer described in ADR-0007: every legal
 * agent method MUST be listed here with its accepted arg set. Unknown
 * methods are rejected. Arguments not on the required|optional list are
 * rejected — this prevents a caller from smuggling surprise fields into
 * a handler that then happens to read them.
 *
 * Building the registry is a one-way migration: every time we convert a
 * method from the ad-hoc shape to a schema'd one, we add it here. The
 * agent's handleAction() can progressively switch to validating via this
 * registry as methods are covered.
 *
 * This class deliberately does NOT itself dispatch — it only validates.
 * Dispatch stays in bin/jabali-agent.
 */
final class MethodRegistry
{
    /**
     * @param  array<string, array{required: list<string>, optional: list<string>}>  $methods
     */
    public function __construct(private readonly array $methods) {}

    /**
     * Default registry for Jabali. New methods are added here as they are
     * hardened. Legacy methods not yet in the schema fall through to the
     * agent's old handleAction() branches and are NOT validated.
     */
    public static function default(): self
    {
        return new self([
            'ping' => [
                'required' => [],
                'optional' => [],
            ],

            // Agent v2 job control (ADR-0007)
            'job.start' => [
                'required' => ['type', 'argv'],
                'optional' => ['id', 'limits', 'payload', 'dedupe_key'],
            ],
            'job.status' => [
                'required' => ['id'],
                'optional' => [],
            ],
            'job.cancel' => [
                'required' => ['id'],
                'optional' => ['grace_sec'],
            ],
            'job.list' => [
                'required' => [],
                'optional' => ['status', 'type', 'limit'],
            ],
            'job.tail' => [
                'required' => ['id'],
                'optional' => ['since'],
            ],
        ]);
    }

    public function isRegistered(string $method): bool
    {
        return isset($this->methods[$method]);
    }

    /**
     * @param  array<string, mixed>  $args
     *
     * @throws InvalidArgumentException
     */
    public function assertValid(string $method, array $args): void
    {
        if (! isset($this->methods[$method])) {
            throw new InvalidArgumentException("unknown method: {$method}");
        }

        $schema = $this->methods[$method];

        foreach ($schema['required'] as $name) {
            if (! array_key_exists($name, $args)) {
                throw new InvalidArgumentException("missing required arg: {$name}");
            }
        }

        $allowed = array_merge($schema['required'], $schema['optional']);
        foreach (array_keys($args) as $name) {
            if (! in_array($name, $allowed, true)) {
                throw new InvalidArgumentException("unexpected arg: {$name}");
            }
        }
    }
}
