import { createRouter, createWebHistory } from 'vue-router'
import LabsPage from '../pages/LabsPage.vue'
import TopologyPage from '../pages/TopologyPage.vue'
import OperationsPage from '../pages/OperationsPage.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/labs' },
    { path: '/labs', component: LabsPage },
    { path: '/topology', component: TopologyPage },
    { path: '/operations', component: OperationsPage },
  ],
})
