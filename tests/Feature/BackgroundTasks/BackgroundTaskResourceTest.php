<?php

declare(strict_types=1);

namespace Tests\Feature\BackgroundTasks;

use App\Enums\BackgroundTaskStatus;
use App\Enums\BackgroundTaskType;
use App\Filament\Admin\Resources\BackgroundTasks\BackgroundTaskResource;
use App\Filament\Admin\Resources\BackgroundTasks\Pages\ListBackgroundTasks;
use App\Models\BackgroundTask;
use App\Models\User;
use App\Services\Agent\AgentClientInterface;
use Filament\Facades\Filament;
use Livewire\Livewire;
use Mockery;
use Tests\TestCase;

class BackgroundTaskResourceTest extends TestCase
{
    private User $admin;

    protected function setUp(): void
    {
        parent::setUp();
        $this->admin = User::factory()->create(['is_admin' => true]);
        $this->actingAs($this->admin, 'admin');

        // Ensure Filament resolves getUrl() / assertCan* through the admin
        // panel, not whichever panel happens to be registered first.
        Filament::setCurrentPanel(Filament::getPanel('admin'));
    }

    protected function tearDown(): void
    {
        Mockery::close();
        $this->admin->delete();
        parent::tearDown();
    }

    public function test_index_page_renders(): void
    {
        $this->get(BackgroundTaskResource::getUrl('index'))
            ->assertSuccessful();
    }

    public function test_table_lists_tasks(): void
    {
        $running = BackgroundTask::factory()->running()->create(['type' => BackgroundTaskType::Backup]);
        $done = BackgroundTask::factory()->done()->create(['type' => BackgroundTaskType::SslIssue]);

        Livewire::test(ListBackgroundTasks::class)
            ->assertCanSeeTableRecords([$running, $done]);

        $running->delete();
        $done->delete();
    }

    public function test_filter_by_status(): void
    {
        $running = BackgroundTask::factory()->running()->create();
        $done = BackgroundTask::factory()->done()->create();

        Livewire::test(ListBackgroundTasks::class)
            ->filterTable('status', BackgroundTaskStatus::Running->value)
            ->assertCanSeeTableRecords([$running])
            ->assertCanNotSeeTableRecords([$done]);

        $running->delete();
        $done->delete();
    }

    public function test_filter_by_type(): void
    {
        $backup = BackgroundTask::factory()->create(['type' => BackgroundTaskType::Backup]);
        $ssl = BackgroundTask::factory()->create(['type' => BackgroundTaskType::SslIssue]);

        Livewire::test(ListBackgroundTasks::class)
            ->filterTable('type', BackgroundTaskType::Backup->value)
            ->assertCanSeeTableRecords([$backup])
            ->assertCanNotSeeTableRecords([$ssl]);

        $backup->delete();
        $ssl->delete();
    }

    public function test_cancel_action_calls_agent(): void
    {
        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')
            ->once()
            ->with('job.cancel', Mockery::type('array'))
            ->andReturn(['success' => true]);
        $this->app->instance(AgentClientInterface::class, $agent);

        $task = BackgroundTask::factory()->running()->create();

        Livewire::test(ListBackgroundTasks::class)
            ->callTableAction('cancel', $task);

        $task->delete();
    }

    public function test_cancel_action_hidden_for_terminal_task(): void
    {
        $task = BackgroundTask::factory()->done()->create();

        Livewire::test(ListBackgroundTasks::class)
            ->assertTableActionHidden('cancel', $task);

        $task->delete();
    }
}
