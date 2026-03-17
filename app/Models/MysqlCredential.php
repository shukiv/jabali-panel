<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Support\Facades\Crypt;

class MysqlCredential extends Model
{
    protected $fillable = ['user_id', 'mysql_username', 'mysql_password_encrypted'];

    public function user()
    {
        return $this->belongsTo(User::class);
    }

    public function setPasswordAttribute($value)
    {
        $this->attributes['mysql_password_encrypted'] = Crypt::encryptString($value);
    }

    public function getPasswordAttribute()
    {
        return Crypt::decryptString($this->attributes['mysql_password_encrypted']);
    }
}
