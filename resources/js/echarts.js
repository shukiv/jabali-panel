import * as echarts from 'echarts';
import 'echarts/theme/shine';

echarts.registerTheme('jabali-dark', {
    darkMode: true,
    color: ['#4992ff', '#7cffb2', '#fddd60', '#ff6e76', '#58d9f9', '#05c091', '#ff8a45', '#8d48e3', '#dd79ff'],
    backgroundColor: 'transparent',
    textStyle: { color: '#B9B8CE' },
    title: { textStyle: { color: '#EEF1FA' }, subtextStyle: { color: '#B9B8CE' } },
    legend: { textStyle: { color: '#B9B8CE' } },
    axisPointer: { lineStyle: { color: '#817f91' }, crossStyle: { color: '#817f91' }, label: { color: '#fff' } },
    categoryAxis: {
        axisLine: { lineStyle: { color: '#B9B8CE' } },
        splitLine: { show: false, lineStyle: { color: '#484753' } },
        splitArea: { areaStyle: { color: ['rgba(255,255,255,0.02)', 'rgba(255,255,255,0.05)'] } },
    },
    valueAxis: {
        axisLine: { lineStyle: { color: '#B9B8CE' } },
        splitLine: { lineStyle: { color: '#484753' } },
        splitArea: { areaStyle: { color: ['rgba(255,255,255,0.02)', 'rgba(255,255,255,0.05)'] } },
    },
    gauge: { title: { color: '#B9B8CE' } },
});

window.echarts = echarts;
