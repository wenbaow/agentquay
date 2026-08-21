cmake_minimum_required(VERSION 3.16)
project(agentquay-cpp VERSION 0.1.1 LANGUAGES CXX)
set(CMAKE_AUTOMOC ON)
set(CMAKE_AUTORCC ON)
set(CMAKE_AUTOUIC ON)
set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)
set(AGENTQUAY_TARGET agentquay)
if(NOT CMAKE_BUILD_TYPE AND NOT CMAKE_CONFIGURATION_TYPES)
    set(CMAKE_BUILD_TYPE Release CACHE STRING "Build type" FORCE)
endif()
find_package(Qt6 6.5 REQUIRED COMPONENTS Core Network WebSockets)
find_package(Qt6 QUIET COMPONENTS Widgets)
if(Qt6Widgets_FOUND)
    set(AGENTQUAY_WITH_WIDGETS ON)
else()
    set(AGENTQUAY_WITH_WIDGETS OFF)
endif()
add_library(${AGENTQUAY_TARGET} STATIC
    src/Protocol.cpp
    src/agent_tool.cpp
    src/JsonSchemaGenerator.cpp
    src/TokenStore.cpp
    src/ConfirmationHandler.cpp
    src/BridgeSpawner.cpp
    src/AgentQuayClient.cpp
)
# AgentQuayClient 是头文件中的 Q_OBJECT 类（include/ 下，AUTOMOC 不自动处理），
# 用 qt6_generate_moc 显式生成元对象代码并编译进库
qt6_generate_moc(
    ${CMAKE_CURRENT_SOURCE_DIR}/include/agentquay/AgentQuayClient.h
    ${CMAKE_CURRENT_BINARY_DIR}/moc_AgentQuayClient.cpp
)
set_source_files_properties(${CMAKE_CURRENT_BINARY_DIR}/moc_AgentQuayClient.cpp PROPERTIES GENERATED TRUE)
target_sources(${AGENTQUAY_TARGET} PRIVATE ${CMAKE_CURRENT_BINARY_DIR}/moc_AgentQuayClient.cpp)
add_library(agentquay::agentquay ALIAS ${AGENTQUAY_TARGET})
target_include_directories(${AGENTQUAY_TARGET} PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>
)
target_link_libraries(${AGENTQUAY_TARGET} PUBLIC Qt6::Core Qt6::Network Qt6::WebSockets)
if(AGENTQUAY_WITH_WIDGETS)
    target_link_libraries(${AGENTQUAY_TARGET} PUBLIC Qt6::Widgets)
    target_compile_definitions(${AGENTQUAY_TARGET} PUBLIC AGENTQUAY_WITH_WIDGETS=1)
endif()
option(AGENTQUAY_BUILD_TESTS "Build tests" ON)
option(AGENTQUAY_BUILD_EXAMPLES "Build examples" ON)
if(AGENTQUAY_BUILD_TESTS)
    enable_testing()
    add_subdirectory(tests)
endif()
if(AGENTQUAY_BUILD_EXAMPLES)
    add_subdirectory(examples/music_app)
endif()
include(GNUInstallDirs)
include(CMakePackageConfigHelpers)

# 打包配置：构建时启用了 Widgets 则消费方也需声明该依赖（导出的 target 会链接 Qt6::Widgets）
if(AGENTQUAY_WITH_WIDGETS)
    set(AGENTQUAY_CONFIG_WIDGETS_DEP
        "find_dependency(Qt6 COMPONENTS Widgets)")
else()
    set(AGENTQUAY_CONFIG_WIDGETS_DEP "")
endif()

install(TARGETS ${AGENTQUAY_TARGET} EXPORT agentquayTargets
    ARCHIVE DESTINATION ${CMAKE_INSTALL_LIBDIR}
    LIBRARY DESTINATION ${CMAKE_INSTALL_LIBDIR}
    RUNTIME DESTINATION ${CMAKE_INSTALL_BINDIR})
install(DIRECTORY include/ DESTINATION ${CMAKE_INSTALL_INCLUDEDIR})
install(EXPORT agentquayTargets FILE agentquayTargets.cmake NAMESPACE agentquay:: DESTINATION ${CMAKE_INSTALL_LIBDIR}/cmake/agentquay)
configure_package_config_file(${CMAKE_CURRENT_SOURCE_DIR}/cmake/agentquayConfig.cmake.in ${CMAKE_CURRENT_BINARY_DIR}/agentquayConfig.cmake INSTALL_DESTINATION ${CMAKE_INSTALL_LIBDIR}/cmake/agentquay)
write_basic_package_version_file(${CMAKE_CURRENT_BINARY_DIR}/agentquayConfigVersion.cmake VERSION ${PROJECT_VERSION} COMPATIBILITY SameMajorVersion)
install(FILES ${CMAKE_CURRENT_BINARY_DIR}/agentquayConfig.cmake ${CMAKE_CURRENT_BINARY_DIR}/agentquayConfigVersion.cmake DESTINATION ${CMAKE_INSTALL_LIBDIR}/cmake/agentquay)
