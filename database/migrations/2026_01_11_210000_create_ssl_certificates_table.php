<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('ssl_certificates', function (Blueprint $table) {
            $table->id();
            $table->foreignId('domain_id')->constrained()->onDelete('cascade');
            $table->enum('type', ['none', 'self_signed', 'lets_encrypt', 'custom'])->default('none');
            $table->enum('status', ['pending', 'active', 'expired', 'failed', 'revoked'])->default('pending');
            $table->string('issuer')->nullable()->comment('Certificate issuer (Let\'s Encrypt, etc.)');
            $table->text('certificate')->nullable()->comment('PEM encoded certificate');
            $table->text('private_key')->nullable()->comment('PEM encoded private key (encrypted)');
            $table->text('ca_bundle')->nullable()->comment('CA bundle / chain');
            $table->timestamp('issued_at')->nullable();
            $table->timestamp('expires_at')->nullable();
            $table->timestamp('last_check_at')->nullable();
            $table->string('last_error')->nullable();
            $table->integer('renewal_attempts')->default(0);
            $table->boolean('auto_renew')->default(true);
            $table->timestamps();

            $table->index(['status', 'expires_at']);
            $table->index('auto_renew');
        });

        // Add ssl_enabled column to domains if not exists
        if (! Schema::hasColumn('domains', 'ssl_enabled')) {
            Schema::table('domains', function (Blueprint $table) {
                $table->boolean('ssl_enabled')->default(false)->after('is_active');
            });
        }
    }

    public function down(): void
    {
        Schema::dropIfExists('ssl_certificates');

        if (Schema::hasColumn('domains', 'ssl_enabled')) {
            Schema::table('domains', function (Blueprint $table) {
                $table->dropColumn('ssl_enabled');
            });
        }
    }
};
