import { createRouter, createWebHistory } from 'vue-router'
import LabsPage from '../pages/LabsPage.vue'
import TopologyPage from '../pages/TopologyPage.vue'
import ProgramsPage from '../pages/ProgramsPage.vue'
import PackagesPage from '../pages/PackagesPage.vue'
import TrafficPage from '../pages/TrafficPage.vue'
import FaultsPage from '../pages/FaultsPage.vue'
import CapturesPage from '../pages/CapturesPage.vue'
import CaptureViewerPage from '../pages/CaptureViewerPage.vue'
import OperationsPage from '../pages/OperationsPage.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/labs' },
    { path: '/labs', component: LabsPage },
    { path: '/topology', component: TopologyPage },
    { path: '/programs', component: ProgramsPage },
    { path: '/packages', component: PackagesPage },
    { path: '/traffic', component: TrafficPage },
    { path: '/faults', component: FaultsPage },
    { path: '/captures', component: CapturesPage },
    { path: '/captures/:labId/:id', component: CaptureViewerPage },
    { path: '/operations', component: OperationsPage },
  ],
})
