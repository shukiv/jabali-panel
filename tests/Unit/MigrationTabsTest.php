<?php

declare(strict_types=1);

namespace Tests\Unit;

use Tests\TestCase;

class MigrationTabsTest extends TestCase
{
    public function test_migration_page_uses_tabs(): void
    {
        $page = file_get_contents(base_path('app/Filament/Admin/Pages/Migration.php'));

        $this->assertIsString($page);
        $this->assertStringContainsString("Tabs::make(__('Migration Type'))", $page);
        $this->assertStringContainsString("->livewireProperty('activeTab')", $page);
        $this->assertStringContainsString("'cpanel' => Tabs\\Tab::make(__('cPanel Migration'))", $page);
        $this->assertStringContainsString("'whm' => Tabs\\Tab::make(__('WHM Migration'))", $page);
        $this->assertStringContainsString("View::make('filament.admin.pages.migration-cpanel-tab')", $page);
        $this->assertStringContainsString("View::make('filament.admin.pages.migration-whm-tab')", $page);
    }

    public function test_migration_tab_views_embed_migration_pages(): void
    {
        $cpanelView = file_get_contents(base_path('resources/views/filament/admin/pages/migration-cpanel-tab.blade.php'));
        $whmView = file_get_contents(base_path('resources/views/filament/admin/pages/migration-whm-tab.blade.php'));

        $this->assertIsString($cpanelView);
        $this->assertIsString($whmView);
        $this->assertStringContainsString(\App\Filament\Admin\Pages\CpanelMigration::class, $cpanelView);
        $this->assertStringContainsString(\App\Filament\Admin\Pages\WhmMigration::class, $whmView);
    }

    public function test_migration_wizard_query_strings_are_unique(): void
    {
        $cpanel = file_get_contents(base_path('app/Filament/Admin/Pages/CpanelMigration.php'));
        $whm = file_get_contents(base_path('app/Filament/Admin/Pages/WhmMigration.php'));

        $this->assertIsString($cpanel);
        $this->assertIsString($whm);
        $this->assertStringContainsString("#[Url(as: 'cpanel-step')]", $cpanel);
        $this->assertStringContainsString("->persistStepInQueryString('cpanel-step')", $cpanel);
        $this->assertStringContainsString("#[Url(as: 'whm-step')]", $whm);
        $this->assertStringContainsString("->persistStepInQueryString('whm-step')", $whm);
    }

    public function test_migration_pages_are_hidden_from_navigation(): void
    {
        $cpanel = file_get_contents(base_path('app/Filament/Admin/Pages/CpanelMigration.php'));
        $whm = file_get_contents(base_path('app/Filament/Admin/Pages/WhmMigration.php'));

        $this->assertIsString($cpanel);
        $this->assertIsString($whm);
        $this->assertStringContainsString('protected static bool $shouldRegisterNavigation = false;', $cpanel);
        $this->assertStringContainsString('protected static bool $shouldRegisterNavigation = false;', $whm);
    }
}
