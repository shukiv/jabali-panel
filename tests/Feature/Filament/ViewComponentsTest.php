<?php

declare(strict_types=1);

namespace Tests\Feature\Filament;

use Tests\TestCase;

class ViewComponentsTest extends TestCase
{
    public function test_trash_viewer_wraps_table_for_small_screens(): void
    {
        $items = [
            [
                'is_dir' => false,
                'name' => 'example.txt',
                'trashed_at' => time(),
                'trash_name' => 'trash_example.txt',
            ],
        ];

        $this->view('filament.jabali.components.trash-viewer', [
            'items' => $items,
        ])->assertSee('overflow-x-auto', false);
    }

    public function test_protected_dir_users_row_wraps_on_small_screens(): void
    {
        $this->view('filament.jabali.components.protected-dir-users', [
            'users' => ['alice'],
            'path' => '/secure',
        ])->assertSee('sm:flex-row', false);
    }
}
