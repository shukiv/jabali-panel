<?php

namespace Tests\Unit\Models;

use App\Models\DnsSetting;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

class DnsSettingTest extends TestCase
{
    use RefreshDatabase;

    public function test_can_get_setting_with_default(): void
    {
        $value = DnsSetting::get('nonexistent_key', 'default_value');

        $this->assertEquals('default_value', $value);
    }

    public function test_can_set_and_get_setting(): void
    {
        DnsSetting::set('test_key', 'test_value');

        $value = DnsSetting::get('test_key');

        $this->assertEquals('test_value', $value);
    }

    public function test_can_update_existing_setting(): void
    {
        DnsSetting::set('update_key', 'initial_value');
        DnsSetting::set('update_key', 'updated_value');

        $value = DnsSetting::get('update_key');

        $this->assertEquals('updated_value', $value);
    }

    public function test_can_store_array_values(): void
    {
        $array = ['key1' => 'value1', 'key2' => 'value2'];

        DnsSetting::set('array_key', json_encode($array));

        $stored = json_decode(DnsSetting::get('array_key'), true);

        $this->assertEquals($array, $stored);
    }
}
