<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    /**
     * Run the migrations.
     */
    public function up(): void
    {
        Schema::create('mailbox_shares', function (Blueprint $table) {
            $table->id();
            $table->foreignId('mailbox_id')->constrained()->onDelete('cascade');
            $table->foreignId('shared_with_mailbox_id')->constrained('mailboxes')->onDelete('cascade');
            $table->string('folder')->default('INBOX');
            $table->string('acl_rights')->default('lrs');
            $table->timestamps();

            $table->unique(['mailbox_id', 'shared_with_mailbox_id', 'folder']);
        });
    }

    /**
     * Reverse the migrations.
     */
    public function down(): void
    {
        Schema::dropIfExists('mailbox_shares');
    }
};
