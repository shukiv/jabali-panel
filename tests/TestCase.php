<?php

declare(strict_types=1);

namespace Tests;

use Illuminate\Foundation\Testing\TestCase as BaseTestCase;

abstract class TestCase extends BaseTestCase
{
    protected function setUp(): void
    {
        parent::setUp();

        // Skip Vite on CI — no manifest is built in the PHP-only job, and
        // tests that render full Filament panel views (Branding,
        // ServerSettings) would otherwise 500 on `Vite manifest not found`.
        // Dev runs still get real asset URLs because withoutVite() only
        // swaps the Vite facade to a no-op for the duration of the test.
        $this->withoutVite();
    }
}
