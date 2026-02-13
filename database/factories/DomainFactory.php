<?php

namespace Database\Factories;

use Illuminate\Database\Eloquent\Factories\Factory;

/**
 * @extends \Illuminate\Database\Eloquent\Factories\Factory<\App\Models\Domain>
 */
class DomainFactory extends Factory
{
    /**
     * Define the model's default state.
     *
     * @return array<string, mixed>
     */
    public function definition(): array
    {
        return [
            'user_id' => \App\Models\User::factory(),
            'domain' => fake()->unique()->domainName(),
            'document_root' => null,
            'ip_address' => null,
            'ipv6_address' => null,
            'is_active' => true,
            'ssl_enabled' => false,
            'directory_index' => 'index.php index.html',
            'page_cache_enabled' => false,
        ];
    }

    /**
     * Indicate that the domain should have SSL enabled.
     */
    public function withSsl(): static
    {
        return $this->state(fn (array $attributes) => [
            'ssl_enabled' => true,
        ]);
    }

    /**
     * Indicate that the domain should be inactive.
     */
    public function inactive(): static
    {
        return $this->state(fn (array $attributes) => [
            'is_active' => false,
        ]);
    }
}
