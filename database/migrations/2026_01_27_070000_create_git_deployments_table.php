<?php

declare(strict_types=1);

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('git_deployments', function (Blueprint $table) {
            $table->id();
            $table->foreignId('user_id')->constrained()->cascadeOnDelete();
            $table->foreignId('domain_id')->constrained()->cascadeOnDelete();
            $table->string('repo_url');
            $table->string('branch')->default('main');
            $table->string('deploy_path');
            $table->boolean('auto_deploy')->default(false);
            $table->text('deploy_script')->nullable();
            $table->string('last_status')->nullable();
            $table->timestamp('last_deployed_at')->nullable();
            $table->text('last_error')->nullable();
            $table->string('secret_token', 80)->unique();
            $table->timestamps();

            $table->index(['user_id', 'domain_id']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('git_deployments');
    }
};
