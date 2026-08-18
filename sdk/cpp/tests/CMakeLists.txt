cmake_minimum_required(VERSION 3.16)
project(agentquay-cpp-tests LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

# Enable Qt AUTOMOC to generate moc files automatically
set(CMAKE_AUTOMOC ON)
set(CMAKE_AUTORCC ON)
set(CMAKE_AUTOUIC ON)

find_package(Qt6 6.5 REQUIRED COMPONENTS Core Network WebSockets Test)

# Schema unit tests (no Bridge required)
add_executable(schema_tests schema_gen_test.cpp)
target_link_libraries(schema_tests PRIVATE
    Qt6::Core
    Qt6::Test
    agentquay::agentquay
)

# End-to-end tests (requires real Go Bridge: AGENTQUAY_BRIDGE_BIN or bridge/dist)
add_executable(e2e_tests e2e_test.cpp)
target_link_libraries(e2e_tests PRIVATE
    Qt6::Core
    Qt6::Network
    Qt6::WebSockets
    Qt6::Test
    agentquay::agentquay
)

add_test(NAME AgentQuaySchemaGen COMMAND schema_tests)
add_test(NAME AgentQuayE2E COMMAND e2e_tests)

# E2E 需要真实 Go Bridge：ctest 运行时优先用环境变量 AGENTQUAY_BRIDGE_BIN，
# 未设置且仓库存在 dist 产物时自动注入（Windows 平台后缀）
if(NOT DEFINED ENV{AGENTQUAY_BRIDGE_BIN})
    file(GLOB _bridge_bins
        "${CMAKE_CURRENT_SOURCE_DIR}/../../bridge/dist/agentquay-windows-amd64.exe"
        "${CMAKE_CURRENT_SOURCE_DIR}/../../bridge/dist/agentquay-linux-amd64"
        "${CMAKE_CURRENT_SOURCE_DIR}/../../bridge/dist/agentquay-darwin-arm64")
    if(_bridge_bins)
        set_tests_properties(AgentQuayE2E PROPERTIES
            ENVIRONMENT "AGENTQUAY_BRIDGE_BIN=${_bridge_bins}")
    endif()
endif()