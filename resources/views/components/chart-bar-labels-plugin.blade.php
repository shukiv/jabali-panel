<script>
window.filamentChartJsPlugins=[{
    id:'barLabels',
    afterDatasetsDraw(chart){
        var texts=chart.data.datasets[0]&&chart.data.datasets[0].barTexts;
        if(!texts)return;
        var ctx=chart.ctx,meta=chart.getDatasetMeta(0);
        var dark=document.documentElement.classList.contains('dark');
        ctx.save();
        ctx.font='bold 13px system-ui,sans-serif';
        ctx.textBaseline='middle';
        meta.data.forEach(function(bar,i){
            var text=texts[i]||'';
            if(!text)return;
            var w=bar.x-bar.base;
            if(w>80){ctx.fillStyle='#fff';ctx.fillText(text,bar.base+8,bar.y)}
            else{ctx.fillStyle=dark?'rgba(255,255,255,0.7)':'rgba(0,0,0,0.6)';ctx.fillText(text,bar.x+6,bar.y)}
        });
        ctx.restore();
    }
}];
</script>
