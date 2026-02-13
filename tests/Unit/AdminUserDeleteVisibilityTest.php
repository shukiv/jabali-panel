<?php

declare(strict_types=1);

namespace Tests\Unit;

use Tests\TestCase;

class AdminUserDeleteVisibilityTest extends TestCase
{
    public function test_primary_admin_delete_action_is_hidden_in_users_table(): void
    {
        $table = file_get_contents(base_path('app/Filament/Admin/Resources/Users/Tables/UsersTable.php'));

        $this->assertIsString($table);
        $this->assertStringContainsString('->visible(fn ($record) => (int) $record->id !== 1)', $table);
    }

    public function test_primary_admin_delete_action_is_hidden_in_edit_page(): void
    {
        $page = file_get_contents(base_path('app/Filament/Admin/Resources/Users/Pages/EditUser.php'));

        $this->assertIsString($page);
        $this->assertStringContainsString('->visible(fn () => (int) $this->record->id !== 1)', $page);
    }

    public function test_primary_admin_is_not_selectable_in_bulk_actions(): void
    {
        $table = file_get_contents(base_path('app/Filament/Admin/Resources/Users/Tables/UsersTable.php'));

        $this->assertIsString($table);
        $this->assertStringContainsString('->checkIfRecordIsSelectableUsing(fn ($record): bool => (int) $record->id !== 1)', $table);
    }

    public function test_users_table_uses_default_bulk_actions(): void
    {
        $table = file_get_contents(base_path('app/Filament/Admin/Resources/Users/Tables/UsersTable.php'));

        $this->assertIsString($table);
        $this->assertStringContainsString('->bulkActions([', $table);
    }
}
